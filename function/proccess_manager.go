package function

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

type PM2Process struct {
	PID       int            `json:"pid"`
	Name      string         `json:"name"`
	FilePath  string         `json:"filePath"`
	Path      string         `json:"path"`
	PM2Env    map[string]any `json:"pm2_env"`
	PM2ID     int            `json:"pm_id"`
	Monit     Pm2Monit       `json:"monit"`
	IsRunning bool           `json:"is_running"`
}

type Pm2Monit struct {
	Memory int `json:"memory"`
	CPU    int `json:"cpu"`
}

// GetPm2Process is the polling function. It never spawns a child process,
// so no console window can appear.
func GetPm2Process() ([]PM2Process, error) {
	return GetPm2ProcessNoSpawn()
}

// GetPm2ProcessFull runs `pm2 jlist` (spawns node, hidden window on Windows).
// Only use it on user actions, never on a timer.
func GetPm2ProcessFull() ([]PM2Process, error) {
	cmd, err := pm2Command("jlist")
	if err != nil {
		return []PM2Process{}, fmt.Errorf("prepare pm2 jlist command: %w", err)
	}
	str, err := cmd.Output()
	if err != nil {
		return []PM2Process{}, fmt.Errorf("run pm2 jlist: %w", err)
	}
	processes, err := parsePM2Processes(stripANSISequences(str))
	if err != nil {
		return []PM2Process{}, fmt.Errorf("parse pm2 jlist output: %w", err)
	}
	return processes, nil
}

// ---------- No-spawn status (reads PM2 pid files) ----------

func pm2HomeDir() string {
	if home := os.Getenv("PM2_HOME"); home != "" {
		return home
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(userHome, ".pm2")
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // FindProcess opens the process, so the PID exists
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func GetPm2ProcessNoSpawn() ([]PM2Process, error) {
	home := pm2HomeDir()
	if home == "" {
		return []PM2Process{}, nil
	}
	dir := filepath.Join(home, "pids")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []PM2Process{}, nil
		}
		return []PM2Process{}, fmt.Errorf("read pm2 pid directory: %w", err)
	}

	dump := readPm2Dump(home)
	processes := []PM2Process{}

	for _, entry := range entries {
		fileName := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(fileName, ".pid") {
			continue
		}
		base := strings.TrimSuffix(fileName, ".pid")
		dash := strings.LastIndex(base, "-")
		if dash <= 0 {
			continue
		}
		pmID, err := strconv.Atoi(base[dash+1:])
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, fileName))
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			continue
		}

		running := pidAlive(pid)
		status := "stopped"
		if running {
			status = "online"
		}

		process := PM2Process{
			PID:       pid,
			Name:      base[:dash],
			PM2ID:     pmID,
			PM2Env:    map[string]any{"status": status},
			IsRunning: running,
		}
		mergePm2Dump(&process, dump)
		processes = append(processes, process)
	}
	return processes, nil
}

// readPm2Dump reads ~/.pm2/dump.pm2 (written by `pm2 save`), keyed by lowercase name.
func readPm2Dump(home string) map[string]map[string]any {
	result := map[string]map[string]any{}
	raw, err := os.ReadFile(filepath.Join(home, "dump.pm2"))
	if err != nil {
		return result
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return result
	}
	for _, item := range items {
		name := mapString(item, "name")
		if name != "" {
			result[strings.ToLower(name)] = item
		}
	}
	return result
}

func mergePm2Dump(process *PM2Process, dump map[string]map[string]any) {
	item, ok := dump[strings.ToLower(process.Name)]
	if !ok {
		return
	}
	for _, key := range []string{"pm_cwd", "pm_exec_path", "script", "cwd"} {
		if value := mapString(item, key); value != "" {
			process.PM2Env[key] = value
		}
	}
	if process.FilePath == "" {
		process.FilePath = mapString(item, "pm_exec_path", "script")
	}
	if process.Path == "" {
		process.Path = mapString(item, "pm_cwd", "cwd")
	}
}

// ---------- User-triggered PM2 actions ----------

func Pm2LogsWithOutput(ctx context.Context, processID int, output io.Writer) error {
	args, err := pm2LogsArgs(processID)
	if err != nil {
		return err
	}
	if output == nil {
		output = io.Discard
	}

	cmd, err := pm2CommandContext(ctx, args...)
	if err != nil {
		return fmt.Errorf("prepare pm2 logs command: %w", err)
	}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("run pm2 logs for process %d: %w", processID, err)
	}
	return nil
}

func Pm2DeployWithOutput(project Project, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	if strings.TrimSpace(project.Name) == "" {
		return fmt.Errorf("project name is required to start a PM2 process")
	}

	workingDir := filepath.Dir(npmPackageManifestPath(project))
	standaloneDir := standaloneProjectDir(project)
	nextStartArgs, useDirectNextStart := pm2NextStartArgs(project)
	useDirectNextStart = useDirectNextStart && runtime.GOOS == "windows"
	if useDirectNextStart && standaloneDir == "" {
		if err := validateNextProductionBuild(workingDir); err != nil {
			return err
		}
	}

	// Full jlist so stopped processes (which have no pid file) are also found.
	processes, err := GetPm2ProcessFull()
	if err != nil {
		return err
	}
	existing := DetectPm2Binding(&State{Pm2ProcessList: processes}, project)
	replaceLegacyNpm := existing != nil && useDirectNextStart && pm2ProcessUsesNpmCommand(*existing)
	if existing != nil && existing.IsRunning && !replaceLegacyNpm {
		return fmt.Errorf("PM2 process %q is already running", existing.Name)
	}

	var args []string
	replaceExisting := false
	if project.NpmPackage != nil {
		configProject := project
		script := "npm"
		scriptArgs := []string{"start"}
		configWorkingDir := workingDir
		switch {
		case standaloneDir != "":
			configWorkingDir = standaloneDir
			switch project.NpmPackage.ProjectType {
			case NextJS:
				script = filepath.Join(standaloneDir, "server.js")
				configProject.EnvironmentVariables, err = nextRuntimeEnvironment(project, workingDir)
			case NuxtJS:
				script = filepath.Join(standaloneDir, "server", "index.mjs")
				configProject.EnvironmentVariables, err = standaloneRuntimeEnvironment(project, workingDir, false)
			}
			if err != nil {
				return err
			}
			scriptArgs = nil
		case useDirectNextStart:
			script, err = nextCLIScriptPath(project)
			if err != nil {
				return err
			}
			scriptArgs = append([]string{"start"}, nextStartArgs...)
		case runtime.GOOS == "windows":
			script, err = npmCLIScriptPath()
			if err != nil {
				return err
			}
		}
		configPath, err := writeProjectEcosystemConfig(configProject, script, scriptArgs, configWorkingDir)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(output, "Generated PM2 ecosystem config: %s\n", configPath)
		args = []string{"start", filepath.Base(configPath)}
		replaceExisting = existing != nil && (!existing.IsRunning || replaceLegacyNpm)
	} else if standaloneDir != "" {
		args = pm2StandaloneArgs()
		workingDir = standaloneDir
	} else {
		npmCLIPath := ""
		if existing == nil && runtime.GOOS == "windows" {
			npmCLIPath, err = npmCLIScriptPath()
			if err != nil {
				return err
			}
		}
		args, err = pm2DeployArgs(project.Name, existing, npmCLIPath)
		if err != nil {
			return err
		}
	}

	if replaceExisting {
		deleteArgs := []string{"delete", strconv.Itoa(existing.PM2ID)}
		_, _ = fmt.Fprintf(output, "$ pm2 %s\n", strings.Join(deleteArgs, " "))
		deleteCmd, err := pm2Command(deleteArgs...)
		if err != nil {
			return fmt.Errorf("prepare PM2 delete command: %w", err)
		}
		deleteCmd.Stdout = output
		deleteCmd.Stderr = output
		if err := deleteCmd.Run(); err != nil {
			return fmt.Errorf("remove stopped PM2 process %q before replacing its launch command: %w", existing.Name, err)
		}
		existing = nil
	}

	_, _ = fmt.Fprintf(output, "$ pm2 %s\n", strings.Join(args, " "))

	cmd, err := pm2Command(args...)
	if err != nil {
		return fmt.Errorf("prepare pm2 command: %w", err)
	}
	cmd.Dir = workingDir
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run pm2 %s in %q: %w", strings.Join(args, " "), cmd.Dir, err)
	}
	return nil
}

func pm2StandaloneArgs() []string {
	return []string{"start", "ecosystem.config.js"}
}

func pm2ProcessUsesNpmCommand(process PM2Process) bool {
	script := mapString(process.PM2Env, "pm_exec_path", "script", "script_path")
	if separator := strings.LastIndexAny(script, `/\`); separator >= 0 {
		script = script[separator+1:]
	}
	switch strings.ToLower(script) {
	case "npm", "npm.cmd", "npm.ps1", "npm-cli.js":
		return true
	case "cmd.exe":
		args, ok := mapValue(process.PM2Env, "args")
		if !ok {
			return false
		}
		command := strings.ToLower(fmt.Sprint(args))
		return strings.Contains(command, "npm") && strings.Contains(command, "start")
	default:
		return false
	}
}

func pm2Command(args ...string) (*exec.Cmd, error) {
	return pm2CommandContext(context.Background(), args...)
}

func pm2CommandContext(ctx context.Context, args ...string) (*exec.Cmd, error) {
	if runtime.GOOS == "windows" {
		nodePath, err := nodeExecutablePath()
		if err != nil {
			return nil, err
		}
		pm2Path, err := pm2CLIScriptPath()
		if err != nil {
			return nil, err
		}
		commandArgs := append([]string{pm2Path}, args...)
		cmd := exec.CommandContext(ctx, nodePath, commandArgs...)
		hideConsoleWindow(cmd)
		return cmd, nil
	}
	return exec.CommandContext(ctx, "pm2", args...), nil
}

func pm2LogsArgs(processID int) ([]string, error) {
	if processID < 0 {
		return nil, fmt.Errorf("invalid PM2 process ID %d", processID)
	}
	return []string{"logs", strconv.Itoa(processID), "--lines", "100"}, nil
}

func pm2DeployArgs(projectName string, existing *PM2Process, npmCLIPath string) ([]string, error) {
	if existing != nil {
		if existing.IsRunning {
			return nil, fmt.Errorf("PM2 process %q is already running", existing.Name)
		}
		return []string{"restart", strconv.Itoa(existing.PM2ID)}, nil
	}
	if runtime.GOOS == "windows" {
		return pm2WindowsNpmStartArgs(projectName, npmCLIPath), nil
	}
	return []string{"start", "npm", "--name", projectName, "--", "start"}, nil
}

func pm2WindowsNpmStartArgs(projectName, npmCLIPath string) []string {
	return []string{"start", npmCLIPath, "--name", projectName, "--", "start"}
}

func pm2WindowsNextStartArgs(projectName, nextCLIPath string, startArgs []string) []string {
	args := []string{"start", nextCLIPath, "--name", projectName, "--", "start"}
	return append(args, startArgs...)
}

func pm2NextStartArgs(project Project) ([]string, bool) {
	if project.NpmPackage == nil || project.NpmPackage.ProjectType != NextJS {
		return nil, false
	}
	script := strings.TrimSpace(project.NpmPackage.Scripts["start"])
	if script == "" {
		return nil, true
	}

	fields := strings.Fields(script)
	for i := 0; i+1 < len(fields); i++ {
		command := strings.Trim(fields[i], `"'`)
		command = strings.ReplaceAll(command, `\`, "/")
		command = command[strings.LastIndex(command, "/")+1:]
		if !strings.EqualFold(command, "next") && !strings.EqualFold(command, "next.cmd") {
			continue
		}
		if fields[i+1] != "start" {
			return nil, false
		}
		args := make([]string, 0, len(fields)-i-2)
		for _, arg := range fields[i+2:] {
			if arg == "&&" || arg == "||" || arg == ";" || arg == "|" || arg == "&" {
				return nil, false
			}
			args = append(args, strings.Trim(arg, `"'`))
		}
		return args, true
	}
	return nil, false
}

func nextCLIScriptPath(project Project) (string, error) {
	dir := filepath.Dir(npmPackageManifestPath(project))
	for {
		cliPath := filepath.Join(dir, "node_modules", "next", "dist", "bin", "next")
		if fileExists(cliPath) {
			absolutePath, err := filepath.Abs(cliPath)
			if err != nil {
				return "", fmt.Errorf("resolve Next.js CLI path %q: %w", cliPath, err)
			}
			return absolutePath, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("Next.js CLI script not found in node_modules above %q", npmPackageManifestPath(project))
}

func nodeExecutablePath() (string, error) {
	nodePath, err := exec.LookPath("node.exe")
	if err != nil {
		return "", fmt.Errorf("find node.exe on PATH: %w", err)
	}
	return nodePath, nil
}

func npmCLIScriptPath() (string, error) {
	npmCommand, err := exec.LookPath("npm.cmd")
	if err != nil {
		return "", fmt.Errorf("find npm.cmd on PATH: %w", err)
	}
	cliPath := filepath.Join(filepath.Dir(npmCommand), "node_modules", "npm", "bin", "npm-cli.js")
	if !fileExists(cliPath) {
		return "", fmt.Errorf("npm CLI script not found at %q", cliPath)
	}
	cliPath, err = filepath.Abs(cliPath)
	if err != nil {
		return "", fmt.Errorf("resolve npm CLI script path: %w", err)
	}
	return cliPath, nil
}

func pm2CLIScriptPath() (string, error) {
	pm2Command, err := exec.LookPath("pm2.cmd")
	if err != nil {
		return "", fmt.Errorf("find pm2.cmd on PATH: %w", err)
	}
	cliPath := filepath.Join(filepath.Dir(pm2Command), "node_modules", "pm2", "bin", "pm2")
	if !fileExists(cliPath) {
		return "", fmt.Errorf("PM2 CLI script not found at %q", cliPath)
	}
	cliPath, err = filepath.Abs(cliPath)
	if err != nil {
		return "", fmt.Errorf("resolve PM2 CLI script path: %w", err)
	}
	return cliPath, nil
}

func parsePM2Processes(data []byte) ([]PM2Process, error) {
	var processes []PM2Process
	if err := json.Unmarshal(data, &processes); err != nil {
		return nil, err
	}
	for i := range processes {
		processes[i].IsRunning = strings.EqualFold(mapString(processes[i].PM2Env, "status"), "online")
	}
	return processes, nil
}

func stripANSISequences(input []byte) []byte {
	output := make([]byte, 0, len(input))
	for i := 0; i < len(input); {
		if input[i] != 0x1b {
			output = append(output, input[i])
			i++
			continue
		}
		if i+1 >= len(input) {
			break
		}

		switch input[i+1] {
		case '[':
			i += 2
			for i < len(input) {
				c := input[i]
				i++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
		case ']', 'P', '^', '_':
			i += 2
			for i < len(input) {
				if input[i] == 0x07 {
					i++
					break
				}
				if input[i] == 0x1b && i+1 < len(input) && input[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		default:
			i += 2
		}
	}
	return output
}

func DetectPm2Binding(state *State, project Project) *PM2Process {
	if state == nil || strings.TrimSpace(project.Name) == "" {
		return nil
	}

	projectPath, err := filepath.Abs(filepath.Join(filepath.Dir(GetConfigPath()), project.Name))
	if err != nil {
		projectPath = filepath.Clean(filepath.Join(filepath.Dir(GetConfigPath()), project.Name))
	}

	names := []string{project.Name}
	if project.NpmPackage != nil {
		names = append(names, project.NpmPackage.Name)
	}

	bestIndex, bestScore := -1, 0
	for i := range state.Pm2ProcessList {
		score := pm2ProjectMatchScore(state.Pm2ProcessList[i], names, projectPath)
		if score > bestScore {
			bestIndex, bestScore = i, score
		}
	}
	if bestIndex < 0 {
		return nil
	}
	return &state.Pm2ProcessList[bestIndex]
}

func pm2ProjectMatchScore(process PM2Process, names []string, projectPath string) int {
	score := 0
	if matchesProjectName(process.Name, names) {
		score = 100
	}
	for _, path := range []string{process.FilePath, process.Path} {
		if pathScore := projectPathMatchScore(projectPath, path, ""); pathScore > score {
			score = pathScore
		}
	}

	environments := []map[string]any{process.PM2Env}
	for _, key := range []string{"env", "env_production", "env_development"} {
		if nested, ok := process.PM2Env[key].(map[string]any); ok {
			environments = append(environments, nested)
		}
	}

	for _, environment := range environments {
		if matchesProjectName(mapString(environment, "name"), names) && score < 100 {
			score = 100
		}

		if binding, ok := mapValue(environment, "BINDING"); ok {
			if bindingScore := pm2BindingMatchScore(binding, names, projectPath); bindingScore > score {
				score = bindingScore
			}
		}

		cwd := mapString(environment, "pm_cwd", "cwd")
		for _, key := range []string{"pm_cwd", "cwd"} {
			if pathScore := projectPathMatchScore(projectPath, mapString(environment, key), ""); pathScore > score {
				score = pathScore
			}
		}
		for _, key := range []string{"pm_exec_path", "script", "script_path", "filePath", "file_path", "path"} {
			if pathScore := projectPathMatchScore(projectPath, mapString(environment, key), cwd); pathScore > score {
				score = pathScore
			}
		}
	}
	return score
}

func pm2BindingMatchScore(binding any, names []string, projectPath string) int {
	switch value := binding.(type) {
	case string:
		if matchesProjectName(value, names) {
			return 200
		}
		if filepath.IsAbs(value) || strings.ContainsAny(value, `/\`) {
			return projectPathMatchScore(projectPath, value, "")
		}
	case map[string]any:
		if matchesProjectName(mapString(value, "name", "project", "projectName", "app"), names) {
			return 200
		}
		for _, key := range []string{"filePath", "file_path", "path", "cwd"} {
			if score := projectPathMatchScore(projectPath, mapString(value, key), ""); score > 0 {
				return score
			}
		}
	}
	return 0
}

func projectPathMatchScore(projectPath, candidate, base string) int {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return 0
	}
	if !filepath.IsAbs(candidate) {
		if base == "" {
			base = projectPath
		}
		candidate = filepath.Join(base, candidate)
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return 0
	}

	relative, err := filepath.Rel(projectPath, candidate)
	if err != nil {
		return 0
	}
	if runtime.GOOS == "windows" {
		relative = strings.ToLower(relative)
	}
	if relative == "." {
		return 300
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return 0
	}
	return 250
}

func matchesProjectName(candidate string, names []string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" && strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

func mapValue(values map[string]any, key string) (any, bool) {
	for name, value := range values {
		if strings.EqualFold(name, key) {
			return value, true
		}
	}
	return nil, false
}

func mapString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := mapValue(values, key); ok {
			if text, ok := value.(string); ok {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}
