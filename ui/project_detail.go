package main

import (
	"context"
	"fmt"
	"image/color"
	"slices"
	"strings"
	"sync"
	"time"

	functions "hollishake/godeploy-function"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// ---- Vercel / Geist dark palette ----
var (
	colBg          = color.NRGBA{R: 0, G: 0, B: 0, A: 255}
	colCard        = color.NRGBA{R: 10, G: 10, B: 10, A: 255}
	colCardBorder  = color.NRGBA{R: 38, G: 38, B: 38, A: 255}
	colDivider     = color.NRGBA{R: 38, G: 38, B: 38, A: 255}
	colTextPrimary = color.NRGBA{R: 237, G: 237, B: 237, A: 255}
	colTextMuted   = color.NRGBA{R: 161, G: 161, B: 170, A: 255}
	colTextFaint   = color.NRGBA{R: 113, G: 113, B: 122, A: 255}
	colAccent      = color.NRGBA{R: 0, G: 112, B: 243, A: 255}
	colSuccess     = color.NRGBA{R: 0, G: 223, B: 130, A: 255}
	colDanger      = color.NRGBA{R: 255, G: 77, B: 79, A: 255}

	// GitHub-style branch pill (blue tint)
	colBranchBg     = color.NRGBA{R: 56, G: 139, B: 253, A: 26}
	colBranchBorder = color.NRGBA{R: 56, G: 139, B: 253, A: 102}
	colBranchText   = color.NRGBA{R: 88, G: 166, B: 255, A: 255}

	// Active status badge (green tint)
	colSuccessBg     = color.NRGBA{R: 0, G: 223, B: 130, A: 20}
	colSuccessBorder = color.NRGBA{R: 0, G: 223, B: 130, A: 70}
)

var activePM2LogListener struct {
	sync.Mutex
	cancel context.CancelFunc
}

func stopActivePM2LogListener() {
	activePM2LogListener.Lock()
	cancel := activePM2LogListener.cancel
	activePM2LogListener.cancel = nil
	activePM2LogListener.Unlock()
	if cancel != nil {
		cancel()
	}
}

// =====================================================================
// Terminal view (io.Writer that safely updates the UI)
// =====================================================================

const (
	terminalMaxLines     = 2000                  // lines kept in memory / shown
	terminalMaxLineBytes = 4096                  // longer lines are truncated
	terminalViewHeight   = 320                   // min height of the log area
	flushInterval        = 50 * time.Millisecond // max ~20 UI refreshes / second
)

type escState uint8

const (
	escNone escState = iota
	escEsc           // saw ESC
	escCSI           // inside ESC [ ... final-byte
	escOSC           // inside ESC ] ... BEL / ESC \
)

// terminalView is a read-only, auto-scrolling log view that implements
// io.Writer.
//
//   - Write is cheap and safe from any goroutine: it only parses bytes into
//     lines (handling \r overwrites and stripping ANSI escapes). It never
//     touches the UI.
//   - UI refreshes are coalesced to at most one per flushInterval.
//   - Lines are rendered by a virtualized widget.List, so only the visible
//     rows are laid out, no matter how much output there is.
type terminalView struct {
	// ---- producer side (guarded by mu) ----
	mu        sync.Mutex
	lines     []string // completed lines
	cur       []byte   // the line currently being written
	cr        bool     // a '\r' was seen; next printable byte overwrites cur
	esc       escState // ANSI escape parser state
	version   uint64   // bumped on every change
	flushed   uint64   // version captured by the last snapshot
	scheduled bool     // a flush is pending or running

	// ---- UI side (only touched on the Fyne thread) ----
	display []string
	rowH    float32
	list    *widget.List
	view    fyne.CanvasObject
}

func newTerminalView(initial string) *terminalView {
	t := &terminalView{}
	t.feedStringLocked(initial) // no lock needed yet: not shared
	t.display = t.snapshotLocked()

	newRow := func() fyne.CanvasObject {
		l := widget.NewLabel("")
		l.TextStyle = fyne.TextStyle{Monospace: true}
		return l
	}
	t.rowH = newRow().MinSize().Height

	t.list = widget.NewList(
		func() int { return len(t.display) },
		newRow,
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id < len(t.display) {
				o.(*widget.Label).SetText(t.display[id])
			}
		},
	)
	// Log lines are not selectable.
	t.list.OnSelected = func(widget.ListItemID) { t.list.UnselectAll() }

	bg := canvas.NewRectangle(colBg)
	bg.CornerRadius = 6
	bg.StrokeColor = colCardBorder
	bg.StrokeWidth = 1

	sizer := canvas.NewRectangle(color.Transparent)
	sizer.SetMinSize(fyne.NewSize(0, terminalViewHeight))

	t.view = container.NewStack(bg, sizer, container.NewPadded(t.list))
	return t
}

// Object returns the widget to place in a layout.
func (t *terminalView) Object() fyne.CanvasObject { return t.view }

// Reset replaces the whole content.
func (t *terminalView) Reset(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.lines)
	t.lines = t.lines[:0]
	t.cur = t.cur[:0]
	t.cr = false
	t.esc = escNone
	t.feedStringLocked(text)
	t.version++
	t.scheduleLocked()
}

// Write implements io.Writer. It never touches the UI directly.
func (t *terminalView) Write(p []byte) (int, error) {
	t.mu.Lock()
	for _, c := range p {
		t.feedLocked(c)
	}
	t.version++
	t.scheduleLocked()
	t.mu.Unlock()
	return len(p), nil
}

func (t *terminalView) feedStringLocked(s string) {
	for i := 0; i < len(s); i++ {
		t.feedLocked(s[i])
	}
}

// feedLocked consumes one byte: strips ANSI escapes, handles \r overwrites,
// tabs and control characters, and splits lines.
func (t *terminalView) feedLocked(c byte) {
	switch t.esc {
	case escEsc:
		switch c {
		case '[':
			t.esc = escCSI
		case ']':
			t.esc = escOSC
		default:
			t.esc = escNone
		}
		return
	case escCSI:
		if c >= 0x40 && c <= 0x7e { // final byte
			t.esc = escNone
		}
		return
	case escOSC:
		switch c {
		case 0x07: // BEL terminator
			t.esc = escNone
		case 0x1b: // possibly ESC \ (string terminator)
			t.esc = escEsc
		}
		return
	}

	switch c {
	case 0x1b:
		t.esc = escEsc
	case '\r':
		t.cr = true // "\r\n" works too: '\n' clears the flag
	case '\n':
		t.cr = false
		t.pushLineLocked()
	case '\t':
		for i := 0; i < 4; i++ {
			t.putLocked(' ')
		}
	default:
		if c >= 0x20 && c != 0x7f {
			t.putLocked(c)
		}
	}
}

func (t *terminalView) putLocked(c byte) {
	if t.cr {
		t.cur = t.cur[:0]
		t.cr = false
	}
	if len(t.cur) < terminalMaxLineBytes {
		t.cur = append(t.cur, c)
	}
}

func (t *terminalView) pushLineLocked() {
	t.lines = append(t.lines, string(t.cur))
	t.cur = t.cur[:0]

	// Trim in batches so the copy cost is amortized.
	if len(t.lines) > terminalMaxLines+terminalMaxLines/4 {
		keep := t.lines[len(t.lines)-terminalMaxLines:]
		n := copy(t.lines, keep)
		clear(t.lines[n:])
		t.lines = t.lines[:n]
	}
}

// snapshotLocked returns an independent copy of the visible lines
// (including the partial last line).
func (t *terminalView) snapshotLocked() []string {
	start := max(0, len(t.lines)-terminalMaxLines)
	out := make([]string, 0, len(t.lines)-start+1)
	out = append(out, t.lines[start:]...)
	if len(t.cur) > 0 {
		out = append(out, string(t.cur))
	}
	return out
}

// scheduleLocked arms one flush if none is pending. Caller must hold t.mu.
func (t *terminalView) scheduleLocked() {
	if t.scheduled {
		return
	}
	t.scheduled = true
	time.AfterFunc(flushInterval, t.flush)
}

// flush runs on a timer goroutine. DoAndWait gives natural backpressure:
// while the UI is busy rendering, new writes only grow the buffer.
func (t *terminalView) flush() {
	t.mu.Lock()
	snap := t.snapshotLocked()
	t.flushed = t.version
	t.mu.Unlock()

	fyne.DoAndWait(func() {
		follow := t.atBottom() // decide using the old content size
		t.display = snap
		t.list.Refresh() // virtualized: only visible rows are updated
		if follow && len(snap) > 0 {
			t.list.ScrollToBottom()
		}
	})

	t.mu.Lock()
	t.scheduled = false
	if t.version != t.flushed { // more output arrived while rendering
		t.scheduleLocked()
	}
	t.mu.Unlock()
}

// atBottom reports whether the view is scrolled to (about) the end, so we
// only auto-follow when the user hasn't scrolled up. UI thread only.
func (t *terminalView) atBottom() bool {
	n := len(t.display)
	if n == 0 {
		return true
	}
	content := float32(n)*t.rowH + float32(n-1)*theme.Padding()
	return t.list.GetScrollOffset()+t.list.Size().Height >= content-t.rowH
}

// =====================================================================
// Deploy status indicator (Idle / Deploying / Done / Failed)
// =====================================================================

type deployStatus struct {
	dot  *canvas.Circle
	text *canvas.Text
	obj  fyne.CanvasObject
}

func newDeployStatus() *deployStatus {
	s := &deployStatus{
		dot:  canvas.NewCircle(colTextFaint),
		text: canvas.NewText("Idle", colTextMuted),
	}
	s.text.TextSize = 12
	s.text.TextStyle = fyne.TextStyle{Bold: true}

	s.obj = container.NewHBox(
		container.NewCenter(container.NewGridWrap(fyne.NewSize(8, 8), s.dot)),
		hSpacer(6),
		container.NewCenter(s.text),
	)
	return s
}

// Set updates the indicator. Safe to call from any goroutine.
func (s *deployStatus) Set(label string, c color.Color) {
	fyne.Do(func() {
		s.dot.FillColor = c
		s.dot.Refresh()
		s.text.Text = label
		s.text.Color = c
		s.text.Refresh()
	})
}

// =====================================================================
// Screens
// =====================================================================

func NewProjectDetail(project functions.Project) fyne.CanvasObject {
	stopActivePM2LogListener()
	terminal := newTerminalView("$ ready to deploy\n")
	status := newDeployStatus()
	npmPackage, npmErr := functions.DetectNpmPackage(project)

	content := container.NewVBox(
		projectHeader(project),
		vSpacer(12),

		card("Repository", "The source this project deploys from.",
			fullRow(field("Repository URL", monoValue(project.RepositoryURL, "Not configured"))),
			vSpacer(10),
			fullRow(field("Production Branch", githubBranchBadge(project.Branch))),
		),
		vSpacer(10),
		npmPackageCard(npmPackage, npmErr),
		vSpacer(10),

		card("Git Identity", "Used for commits made by deployments.",
			columns(
				field("Username", monoValue(project.GitUsername, "Not configured")),
				field("Email", monoValue(project.GitEmail, "Not configured")),
			),
		),
		vSpacer(10),

		card("Credentials", "Only the last 4 characters of the token are shown.",
			fullRow(field("Personal Access Token", monoValue(maskSecret(project.PersonalAccessToken), "Not configured"))),
		),
		vSpacer(10),

		card("Deployment output", "Live output from the deployment command.",
			status.obj,
			vSpacer(8),
			terminal.Object(),
		),
	)

	padded := container.New(layout.NewCustomPaddedLayout(16, 16, 20, 20), content)
	details := container.NewVScroll(padded)

	var deploy *widget.Button
	processRunning := project.PM2Process != nil && project.PM2Process.IsRunning
	if process := project.PM2Process; process != nil {
		if process.IsRunning {
			status.Set("Running", colSuccess)
		} else if pm2Status, ok := process.PM2Env["status"].(string); ok && strings.TrimSpace(pm2Status) != "" {
			statusLabel := strings.TrimSpace(pm2Status)
			status.Set(strings.ToUpper(statusLabel[:1])+statusLabel[1:], colDanger)
		} else {
			status.Set("Stopped", colTextMuted)
		}

		processID := process.PM2ID
		terminal.Reset(fmt.Sprintf("$ pm2 logs %d --lines 100\n", processID))
		ctx, cancel := context.WithCancel(context.Background())
		activePM2LogListener.Lock()
		activePM2LogListener.cancel = cancel
		activePM2LogListener.Unlock()
		go func() {
			if err := functions.Pm2LogsWithOutput(ctx, processID, terminal); err != nil {
				_, _ = fmt.Fprintf(terminal, "\nERROR: %v\n", err)
			}
		}()
	}
	deploy = widget.NewButtonWithIcon("Deploy", theme.UploadIcon(), func() {
		deploy.Disable()
		deploy.SetText("Deploying...")
		status.Set("Deploying...", colAccent)
		terminal.Reset("$ starting deployment...\n")

		go func() {
			var err error

			// Always finish: update status + re-enable the button, even on panic.
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v", r)
				}

				if err != nil {
					_, _ = terminal.Write([]byte("\nERROR: " + err.Error() + "\n"))
					status.Set("Failed", colDanger)
				} else {
					_, _ = terminal.Write([]byte("\nDeployment completed.\n"))
					status.Set("Done", colSuccess)
				}
				fyne.Do(func() {
					if err == nil {
						deploy.SetText("Running")
						deploy.Disable()
					} else {
						deploy.SetText("Deploy")
						deploy.Enable()
					}
				})
			}()

			// Run each deployment step only after the previous one succeeds.
			err = functions.FetchRepositoryWithOutput(project, terminal)
			if err == nil {
				project.NpmPackage, err = functions.DetectNpmPackage(project)
			}
			if err == nil {
				var envPath string
				envPath, err = functions.WriteProjectEnvironmentFile(project)
				if err == nil && envPath != "" {
					_, _ = fmt.Fprintf(terminal, "Wrote project environment file: %s\n", envPath)
				}
			}
			if err == nil {
				err = functions.NpmInstallWithOutput(project, terminal)
			}
			if err == nil {
				err = functions.NpmBuildWithOutput(project, terminal)
			}
			if err == nil {
				err = functions.Pm2DeployWithOutput(project, terminal)
			}
		}()
	})
	if processRunning {
		deploy.SetText("Running")
		deploy.Disable()
	}

	return container.NewBorder(
		nil,
		container.NewPadded(container.NewHBox(layout.NewSpacer(), deploy)),
		nil,
		nil,
		details,
	)
}

func npmPackageCard(pkg *functions.NpmPackage, err error) fyne.CanvasObject {
	if err != nil {
		return card(
			"NPM package",
			"Package metadata detected from package.json.",
			field("Status", monoValue("Not detected", "")),
		)
	}

	scripts := make([]string, 0, len(pkg.Scripts))
	for name := range pkg.Scripts {
		scripts = append(scripts, name)
	}
	slices.Sort(scripts)

	var scriptContent fyne.CanvasObject
	if len(scripts) == 0 {
		scriptContent = monoValue("", "No scripts configured")
	} else {
		scriptRows := make([]fyne.CanvasObject, 0, len(scripts)*2+2)
		scriptRows = append(scriptRows, container.NewGridWithColumns(
			2,
			sectionLabel("Script"),
			sectionLabel("Command"),
		), vSpacer(6))
		for _, name := range scripts {
			scriptRows = append(scriptRows, container.NewGridWithColumns(
				2,
				monoValue(name, ""),
				monoValue(pkg.Scripts[name], ""),
			), vSpacer(6))
		}
		scriptContent = container.NewVBox(scriptRows...)
	}

	return card(
		"NPM package",
		"Package metadata detected from package.json.",
		columns(
			field("Name", monoValue(pkg.Name, "Unnamed package")),
			field("Version", monoValue(pkg.Version, "Not configured")),
		),
		vSpacer(8),
		field("Project type", monoValue(string(pkg.ProjectType), "vanilla")),
		vSpacer(8),
		field("Module type", monoValue(string(pkg.Type), "commonjs")),
		vSpacer(8),
		widget.NewAccordion(widget.NewAccordionItem(
			fmt.Sprintf("Scripts (%d)", len(scripts)),
			scriptContent,
		)),
	)
}

func NewEmptyProjectDetail() fyne.CanvasObject {
	// rounded square with an outlined circle inside
	box := canvas.NewRectangle(colCard)
	box.CornerRadius = 10
	box.StrokeColor = colCardBorder
	box.StrokeWidth = 1

	ring := canvas.NewCircle(color.Transparent)
	ring.StrokeColor = colTextFaint
	ring.StrokeWidth = 1.5

	iconBox := container.NewStack(
		container.NewGridWrap(fyne.NewSize(48, 48), box),
		container.NewCenter(container.NewGridWrap(fyne.NewSize(18, 18), ring)),
	)

	title := canvas.NewText("No project selected", colTextPrimary)
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.TextSize = 15
	title.Alignment = fyne.TextAlignCenter

	subtitle := canvas.NewText("Choose a project from the sidebar to view its settings.", colTextMuted)
	subtitle.TextSize = 12
	subtitle.Alignment = fyne.TextAlignCenter

	body := container.NewVBox(
		container.NewCenter(iconBox),
		vSpacer(8),
		title,
		vSpacer(2),
		subtitle,
	)
	return container.NewCenter(body)
}

// =====================================================================
// Header
// =====================================================================

func projectHeader(project functions.Project) fyne.CanvasObject {
	name := canvas.NewText(project.Name, colTextPrimary)
	name.TextStyle = fyne.TextStyle{Bold: true}
	name.TextSize = 24

	repo := strings.TrimSpace(project.RepositoryURL)
	if repo == "" {
		repo = "No repository connected"
	}
	subtitle := widget.NewLabel(repo)
	subtitle.Wrapping = fyne.TextWrapBreak
	subtitle.TextStyle = fyne.TextStyle{Monospace: true}

	left := container.NewVBox(name, vSpacer(4), subtitle)

	return container.NewBorder(nil, nil, left, container.NewCenter(statusBadge("Active")))
}

// statusBadge: green dot + label inside a tinted rounded pill.
func statusBadge(label string) fyne.CanvasObject {
	bg := canvas.NewRectangle(colSuccessBg)
	bg.CornerRadius = 12
	bg.StrokeColor = colSuccessBorder
	bg.StrokeWidth = 1

	dot := canvas.NewCircle(colSuccess)

	text := canvas.NewText(label, colSuccess)
	text.TextSize = 12
	text.TextStyle = fyne.TextStyle{Bold: true}

	row := container.NewHBox(
		container.NewCenter(container.NewGridWrap(fyne.NewSize(6, 6), dot)),
		hSpacer(6),
		container.NewCenter(text),
	)
	inner := container.New(layout.NewCustomPaddedLayout(4, 4, 10, 12), row)
	return container.NewStack(bg, inner)
}

// =====================================================================
// Card (Vercel-style: title + description, divider, body)
// =====================================================================

func card(title, description string, body ...fyne.CanvasObject) fyne.CanvasObject {
	bg := canvas.NewRectangle(colCard)
	bg.CornerRadius = 8
	bg.StrokeColor = colCardBorder
	bg.StrokeWidth = 1

	t := canvas.NewText(title, colTextPrimary)
	t.TextStyle = fyne.TextStyle{Bold: true}
	t.TextSize = 15

	d := canvas.NewText(description, colTextMuted)
	d.TextSize = 12

	header := container.New(
		layout.NewCustomPaddedLayout(12, 12, 16, 16),
		container.NewVBox(t, vSpacer(4), d),
	)

	content := container.New(
		layout.NewCustomPaddedLayout(12, 14, 16, 16),
		container.NewVBox(body...),
	)

	return container.NewStack(bg, container.NewVBox(header, divider(), content))
}

// =====================================================================
// Fields
// =====================================================================

func field(label string, value fyne.CanvasObject) fyne.CanvasObject {
	lbl := canvas.NewText(label, colTextMuted)
	lbl.TextSize = 12

	return container.NewVBox(lbl, vSpacer(3), value)
}

func sectionLabel(text string) fyne.CanvasObject {
	label := widget.NewLabel(text)
	label.Importance = widget.LowImportance
	return label
}

func fullRow(obj fyne.CanvasObject) fyne.CanvasObject { return obj }

func columns(fields ...fyne.CanvasObject) fyne.CanvasObject {
	return container.NewGridWithColumns(len(fields), fields...)
}

func monoValue(value, fallback string) fyne.CanvasObject {
	value = strings.TrimSpace(value)
	empty := value == ""
	if empty {
		value = fallback
	}
	t := widget.NewLabel(value)
	t.Wrapping = fyne.TextWrapBreak
	t.TextStyle = fyne.TextStyle{Monospace: true}
	if empty {
		t.Importance = widget.LowImportance
	}
	return t
}

// =====================================================================
// GitHub-style branch pill
// =====================================================================

func githubBranchBadge(branch string) fyne.CanvasObject {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = "not configured"
	}

	bg := canvas.NewRectangle(colBranchBg)
	bg.CornerRadius = 12
	bg.StrokeColor = colBranchBorder
	bg.StrokeWidth = 1

	text := canvas.NewText(branch, colBranchText)
	text.TextSize = 12
	text.TextStyle = fyne.TextStyle{Monospace: true}

	row := container.NewHBox(
		container.NewCenter(branchIcon(colBranchText)),
		hSpacer(6),
		container.NewCenter(text),
	)
	inner := container.New(layout.NewCustomPaddedLayout(4, 4, 10, 12), row)
	pill := container.NewStack(bg, inner)

	// HBox keeps the pill at its natural size instead of stretching full width
	return container.NewHBox(pill)
}

// branchIcon draws a small git-branch glyph (no font dependency).
func branchIcon(c color.Color) fyne.CanvasObject {
	const w, h = 12, 14

	ring := func(cx, cy float32) *canvas.Circle {
		ci := canvas.NewCircle(color.Transparent)
		ci.StrokeColor = c
		ci.StrokeWidth = 1.5
		ci.Move(fyne.NewPos(cx-2, cy-2))
		ci.Resize(fyne.NewSize(4, 4))
		return ci
	}
	seg := func(x1, y1, x2, y2 float32) *canvas.Line {
		l := canvas.NewLine(c)
		l.StrokeWidth = 1.5
		l.Position1 = fyne.NewPos(x1, y1)
		l.Position2 = fyne.NewPos(x2, y2)
		return l
	}

	glyph := container.NewWithoutLayout(
		seg(2.5, 4.5, 2.5, 9.5), // main stem
		seg(9.5, 4.5, 9.5, 6.0), // right stem
		seg(9.5, 6.0, 2.5, 9.5), // merge diagonal
		ring(2.5, 2.5),          // top-left node
		ring(2.5, 11.5),         // bottom-left node
		ring(9.5, 2.5),          // top-right node
	)

	spacer := canvas.NewRectangle(color.Transparent)
	return container.NewStack(container.NewGridWrap(fyne.NewSize(w, h), spacer), glyph)
}

// =====================================================================
// Primitives
// =====================================================================

// divider is a 1px line that stretches to the full available width.
func divider() fyne.CanvasObject {
	r := canvas.NewRectangle(colDivider)
	return container.NewGridWrap(fyne.NewSize(1, 1), r)
}

func vSpacer(h float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	return container.NewGridWrap(fyne.NewSize(1, h), r)
}

func hSpacer(w float32) fyne.CanvasObject {
	r := canvas.NewRectangle(color.Transparent)
	return container.NewGridWrap(fyne.NewSize(w, 1), r)
}

func maskSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	if len(secret) <= 4 {
		return "••••"
	}
	return strings.Repeat("•", len(secret)-4) + secret[len(secret)-4:]
}
