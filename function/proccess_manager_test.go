package function

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestPM2CommandUsesPlatformCLI(t *testing.T) {
	command, err := pm2Command("jlist")
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("PM2 is unavailable on PATH: %v", err)
		}
		t.Fatalf("pm2Command() error = %v", err)
	}
	if runtime.GOOS == "windows" {
		if len(command.Args) != 3 || filepath.Base(command.Args[0]) != "node.exe" || filepath.Base(command.Args[1]) != "pm2" || command.Args[2] != "jlist" {
			t.Fatalf("pm2Command() args = %#v, want node.exe <pm2-cli> jlist", command.Args)
		}
		return
	}
	want := []string{"pm2", "jlist"}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("pm2Command() args = %#v, want %#v", command.Args, want)
	}
}

func TestPM2LogsArgs(t *testing.T) {
	args, err := pm2LogsArgs(7)
	if err != nil {
		t.Fatalf("pm2LogsArgs() error = %v", err)
	}
	want := []string{"logs", "7", "--lines", "100"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("pm2LogsArgs() = %#v, want %#v", args, want)
	}
	if _, err := pm2LogsArgs(-1); err == nil {
		t.Fatal("pm2LogsArgs(-1) error = nil, want an error")
	}
}

func TestPM2StandaloneArgs(t *testing.T) {
	got := pm2StandaloneArgs()
	want := []string{"start", "ecosystem.config.js"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pm2StandaloneArgs() = %#v, want %#v", got, want)
	}
}

func TestPM2WindowsNpmStartArgs(t *testing.T) {
	npmCLIPath := `C:\nodejs\node_modules\npm\bin\npm-cli.js`
	got := pm2WindowsNpmStartArgs("site", npmCLIPath)
	want := []string{"start", npmCLIPath, "--name", "site", "--", "start"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pm2WindowsNpmStartArgs() = %#v, want %#v", got, want)
	}
}

func TestPM2ProcessUsesNpmCommand(t *testing.T) {
	tests := []struct {
		name    string
		process PM2Process
		want    bool
	}{
		{
			name:    "Windows npm command",
			process: PM2Process{PM2Env: map[string]any{"pm_exec_path": `C:\nodejs\NPM.CMD`}},
			want:    true,
		},
		{
			name:    "Node-launched npm CLI",
			process: PM2Process{PM2Env: map[string]any{"pm_exec_path": `C:\nodejs\node_modules\npm\bin\npm-cli.js`}},
			want:    true,
		},
		{
			name: "legacy cmd wrapper around npm",
			process: PM2Process{PM2Env: map[string]any{
				"pm_exec_path": `C:\Windows\System32\cmd.exe`,
				"args":         []any{"/c", "npm", "start"},
			}},
			want: true,
		},
		{
			name:    "Next.js executable",
			process: PM2Process{PM2Env: map[string]any{"pm_exec_path": `C:\project\node_modules\next\dist\bin\next`}},
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pm2ProcessUsesNpmCommand(tt.process); got != tt.want {
				t.Errorf("pm2ProcessUsesNpmCommand() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestPM2NextStartArgs(t *testing.T) {
	tests := []struct {
		name        string
		projectType NpmProjectType
		script      string
		wantArgs    []string
		wantDirect  bool
	}{
		{
			name:        "reads Next start options",
			projectType: NextJS,
			script:      "next start -p 3001",
			wantArgs:    []string{"-p", "3001"},
			wantDirect:  true,
		},
		{
			name:        "supports a Next binary path",
			projectType: NextJS,
			script:      "node_modules/.bin/next start --port 3001",
			wantArgs:    []string{"--port", "3001"},
			wantDirect:  true,
		},
		{
			name:        "uses Next default start when script is missing",
			projectType: NextJS,
			wantDirect:  true,
		},
		{
			name:        "does not replace custom start scripts",
			projectType: NextJS,
			script:      "node server.js",
		},
		{
			name:        "does not use Next for other frameworks",
			projectType: Vite,
			script:      "next start -p 3001",
		},
		{
			name:        "does not pass shell chains to Next",
			projectType: NextJS,
			script:      "next start && node post-start.js",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArgs, gotDirect := pm2NextStartArgs(Project{
				NpmPackage: &NpmPackage{
					ProjectType: tt.projectType,
					Scripts:     map[string]string{"start": tt.script},
				},
			})
			if gotDirect != tt.wantDirect {
				t.Fatalf("pm2NextStartArgs() direct = %t, want %t", gotDirect, tt.wantDirect)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("pm2NextStartArgs() args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestPM2WindowsNextStartArgs(t *testing.T) {
	nextCLIPath := `C:\project\node_modules\next\dist\bin\next`
	got := pm2WindowsNextStartArgs("site", nextCLIPath, []string{"-p", "3001"})
	want := []string{"start", nextCLIPath, "--name", "site", "--", "start", "-p", "3001"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pm2WindowsNextStartArgs() = %#v, want %#v", got, want)
	}
}

func TestNextCLIScriptPathFindsWorkspaceDependency(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "apps", "site", "package.json")
	want := filepath.Join(root, "node_modules", "next", "dist", "bin", "next")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatalf("create Next.js CLI directory: %v", err)
	}
	if err := os.WriteFile(want, []byte(""), 0o600); err != nil {
		t.Fatalf("write Next.js CLI script: %v", err)
	}

	got, err := nextCLIScriptPath(Project{NpmPackage: &NpmPackage{PackagePath: manifestPath}})
	if err != nil {
		t.Fatalf("nextCLIScriptPath() error = %v", err)
	}
	absoluteWant, err := filepath.Abs(want)
	if err != nil {
		t.Fatalf("resolve expected Next.js CLI path: %v", err)
	}
	if got != absoluteWant {
		t.Errorf("nextCLIScriptPath() = %q, want %q", got, absoluteWant)
	}
}

func TestStripANSISequencesAllowsPM2JSONParsing(t *testing.T) {
	input := []byte("\x1b[32m[{\"name\":\"api\"}]\x1b[0m")
	processes, err := parsePM2Processes(stripANSISequences(input))
	if err != nil {
		t.Fatalf("parsePM2Processes() error = %v", err)
	}
	if len(processes) != 1 || processes[0].Name != "api" {
		t.Fatalf("parsed processes = %#v, want one process named api", processes)
	}
}

func TestParsePM2ProcessesSetsRunningFromStatus(t *testing.T) {
	input := []byte(`[
		{"name":"online-app","pm2_env":{"status":"online"}},
		{"name":"stopped-app","pm2_env":{"status":"stopped"}},
		{"name":"missing-status","pm2_env":{}}
	]`)

	processes, err := parsePM2Processes(input)
	if err != nil {
		t.Fatalf("parsePM2Processes() error = %v", err)
	}
	if len(processes) != 3 {
		t.Fatalf("parsed %d processes, want 3", len(processes))
	}

	wantRunning := map[string]bool{
		"online-app":     true,
		"stopped-app":    false,
		"missing-status": false,
	}
	for _, process := range processes {
		want, ok := wantRunning[process.Name]
		if !ok {
			t.Errorf("unexpected process %q", process.Name)
			continue
		}
		if process.IsRunning != want {
			t.Errorf("process %q IsRunning = %t, want %t", process.Name, process.IsRunning, want)
		}
		delete(wantRunning, process.Name)
	}
	for name := range wantRunning {
		t.Errorf("process %q not found", name)
	}
}

func TestPM2DeployArgs(t *testing.T) {
	newProcessArgs := []string{"start", "npm", "--name", "site", "--", "start"}
	npmCLIPath := `C:\nvm4w\nodejs\node_modules\npm\bin\npm-cli.js`
	if runtime.GOOS == "windows" {
		newProcessArgs = pm2WindowsNpmStartArgs("site", npmCLIPath)
	}
	tests := []struct {
		name        string
		projectName string
		existing    *PM2Process
		wantArgs    []string
		wantErr     bool
	}{
		{
			name:        "starts a new process",
			projectName: "site",
			wantArgs:    newProcessArgs,
		},
		{
			name:        "restarts a stopped process",
			projectName: "site",
			existing:    &PM2Process{PM2ID: 7},
			wantArgs:    []string{"restart", "7"},
		},
		{
			name:        "rejects an already running process",
			projectName: "site",
			existing:    &PM2Process{Name: "site", IsRunning: true},
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pm2DeployArgs(tt.projectName, tt.existing, npmCLIPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("pm2DeployArgs() error = %v, wantErr %t", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.wantArgs) {
				t.Errorf("pm2DeployArgs() = %#v, want %#v", got, tt.wantArgs)
			}
		})
	}
}

func TestDetectPm2BindingMatchesProjectIdentityAndPaths(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())

	project := Project{
		Name:       "site",
		NpmPackage: &NpmPackage{Name: "@acme/site"},
	}
	projectPath := filepath.Join(filepath.Dir(GetConfigPath()), project.Name)

	tests := []struct {
		name      string
		processes []PM2Process
		wantIndex int
	}{
		{
			name:      "process name",
			processes: []PM2Process{{Name: "SITE"}},
			wantIndex: 0,
		},
		{
			name:      "package name in PM2 environment",
			processes: []PM2Process{{PM2Env: map[string]any{"name": "@acme/site"}}},
			wantIndex: 0,
		},
		{
			name:      "binding name",
			processes: []PM2Process{{PM2Env: map[string]any{"BINDING": "site"}}},
			wantIndex: 0,
		},
		{
			name: "binding file path",
			processes: []PM2Process{{
				PM2Env: map[string]any{
					"BINDING": map[string]any{"filePath": filepath.Join(projectPath, "dist", "server.js")},
				},
			}},
			wantIndex: 0,
		},
		{
			name:      "PM2 working directory",
			processes: []PM2Process{{PM2Env: map[string]any{"pm_cwd": projectPath}}},
			wantIndex: 0,
		},
		{
			name:      "relative PM2 executable path",
			processes: []PM2Process{{PM2Env: map[string]any{"pm_exec_path": "dist/server.js"}}},
			wantIndex: 0,
		},
		{
			name: "project path outranks a matching name",
			processes: []PM2Process{
				{Name: "site", Path: filepath.Join(filepath.Dir(projectPath), "other")},
				{Path: filepath.Join(projectPath, "dist", "server.js")},
			},
			wantIndex: 1,
		},
		{
			name:      "unrelated binding value is not a match",
			processes: []PM2Process{{PM2Env: map[string]any{"BINDING": true}}},
			wantIndex: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &State{Pm2ProcessList: tt.processes}
			got := DetectPm2Binding(state, project)
			if tt.wantIndex == -1 {
				if got != nil {
					t.Fatalf("DetectPm2Binding() = %#v, want nil", got)
				}
				return
			}
			if got != &state.Pm2ProcessList[tt.wantIndex] {
				t.Fatalf("DetectPm2Binding() = %#v, want process at index %d", got, tt.wantIndex)
			}
		})
	}
}
