package function

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestStripANSISequencesAllowsPM2JSONParsing(t *testing.T) {
	input := []byte("\x1b[32m[{\"name\":\"api\"}]\x1b[0m")
	cleaned := stripANSISequences(input)

	var processes []PM2Process
	if err := json.Unmarshal(cleaned, &processes); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(processes) != 1 || processes[0].Name != "api" {
		t.Fatalf("parsed processes = %#v, want one process named api", processes)
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
