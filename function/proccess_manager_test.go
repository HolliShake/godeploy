package function

import (
	"path/filepath"
	"reflect"
	"testing"
)

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
			wantArgs:    []string{"start", "npm", "--name", "site", "--", "start"},
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
			got, err := pm2DeployArgs(tt.projectName, tt.existing)
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
