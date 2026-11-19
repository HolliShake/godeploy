package function

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

func GetPm2Process() ([]PM2Process, error) {
	str, err := exec.Command("pm2", "jlist").Output()
	if err != nil {
		return []PM2Process{}, fmt.Errorf("run pm2 jlist: %w", err)
	}
	processes, err := parsePM2Processes(stripANSISequences(str))
	if err != nil {
		return []PM2Process{}, fmt.Errorf("parse pm2 jlist output: %w", err)
	}
	return processes, nil
}

func Pm2DeployWithOutput(project Project, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	if strings.TrimSpace(project.Name) == "" {
		return fmt.Errorf("project name is required to start a PM2 process")
	}

	processes, err := GetPm2Process()
	if err != nil {
		return err
	}
	existing := DetectPm2Binding(&State{Pm2ProcessList: processes}, project)
	args, err := pm2DeployArgs(project.Name, existing)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(output, "$ pm2 %s\n", strings.Join(args, " "))

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", append([]string{"/c", "pm2"}, args...)...)
	} else {
		cmd = exec.Command("pm2", args...)
	}
	cmd.Dir = filepath.Dir(npmPackageManifestPath(project))
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run pm2 %s in %q: %w", strings.Join(args, " "), cmd.Dir, err)
	}
	return nil
}

func pm2DeployArgs(projectName string, existing *PM2Process) ([]string, error) {
	if existing != nil {
		if existing.IsRunning {
			return nil, fmt.Errorf("PM2 process %q is already running", existing.Name)
		}
		return []string{"restart", strconv.Itoa(existing.PM2ID)}, nil
	}
	return []string{"start", "npm", "--name", projectName, "--", "start"}, nil
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
