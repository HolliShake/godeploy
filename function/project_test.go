package function

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseEnvironmentFile(t *testing.T) {
	input := `# defaults
export APP_ENV=production
PORT=8080
EMPTY=
DOUBLE_QUOTED="hello world\nnext"
SINGLE_QUOTED='literal # value'
TOKEN=abc=def # inline comment
URL=https://example.test/#fragment
`
	got, err := ParseEnvironmentFile(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseEnvironmentFile() error = %v", err)
	}

	want := map[string]string{
		"APP_ENV":       "production",
		"PORT":          "8080",
		"EMPTY":         "",
		"DOUBLE_QUOTED": "hello world\nnext",
		"SINGLE_QUOTED": "literal # value",
		"TOKEN":         "abc=def",
		"URL":           "https://example.test/#fragment",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseEnvironmentFile() = %#v, want %#v", got, want)
	}
}

func TestParseEnvironmentFileRejectsMalformedLines(t *testing.T) {
	for _, input := range []string{
		"INVALID_LINE",
		"1BAD=value",
		"VALUE=\"unterminated",
		"VALUE='ok' trailing",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseEnvironmentFile(strings.NewReader(input)); err == nil {
				t.Fatal("ParseEnvironmentFile() error = nil, want an error")
			}
		})
	}
}

func TestWriteProjectEnvironmentFileWritesBesidePackageManifest(t *testing.T) {
	projectPath := t.TempDir()
	manifestPath := filepath.Join(projectPath, "package.json")
	wantPath := filepath.Join(projectPath, ".env")
	if err := os.WriteFile(wantPath, []byte("OLD=value\n"), 0o600); err != nil {
		t.Fatalf("create existing environment file: %v", err)
	}

	project := Project{
		NpmPackage: &NpmPackage{PackagePath: manifestPath},
		EnvironmentVariables: map[string]string{
			"APP_ENV":  "production",
			"PASSWORD": "value with spaces\nand a newline",
		},
	}
	path, err := WriteProjectEnvironmentFile(project)
	if err != nil {
		t.Fatalf("WriteProjectEnvironmentFile() error = %v", err)
	}
	if path != wantPath {
		t.Fatalf("WriteProjectEnvironmentFile() path = %q, want %q", path, wantPath)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open generated environment file: %v", err)
	}
	defer file.Close()

	got, err := ParseEnvironmentFile(file)
	if err != nil {
		t.Fatalf("parse generated environment file: %v", err)
	}
	if !reflect.DeepEqual(got, project.EnvironmentVariables) {
		t.Errorf("generated environment variables = %#v, want %#v", got, project.EnvironmentVariables)
	}
}

func TestWriteProjectEnvironmentFileMergesSelectedProfile(t *testing.T) {
	t.Setenv("ENV", "")
	projectPath := t.TempDir()
	profilePath := filepath.Join(projectPath, ".env.production")
	if err := os.WriteFile(profilePath, []byte("PORT=3001\nPROFILE_VALUE=from-profile\n"), 0o600); err != nil {
		t.Fatalf("write environment profile: %v", err)
	}
	project := Project{
		NpmPackage: &NpmPackage{PackagePath: filepath.Join(projectPath, "package.json")},
		EnvironmentVariables: map[string]string{
			"ENV":  "production",
			"PORT": "3002",
		},
	}
	if _, err := WriteProjectEnvironmentFile(project); err != nil {
		t.Fatalf("WriteProjectEnvironmentFile() error = %v", err)
	}

	file, err := os.Open(filepath.Join(projectPath, ".env"))
	if err != nil {
		t.Fatalf("open generated environment file: %v", err)
	}
	got, parseErr := ParseEnvironmentFile(file)
	closeErr := file.Close()
	if parseErr != nil {
		t.Fatalf("parse generated environment file: %v", parseErr)
	}
	if closeErr != nil {
		t.Fatalf("close generated environment file: %v", closeErr)
	}
	if got["PORT"] != "3002" || got["PROFILE_VALUE"] != "from-profile" || got["ENV"] != "production" {
		t.Errorf("generated environment variables = %#v", got)
	}
}

func TestFetchRepositoryUpdatesExistingCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	configRoot := t.TempDir()
	t.Setenv("APPDATA", configRoot)
	dataDir := filepath.Join(configRoot, AppName)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("create GoDeploy data directory: %v", err)
	}
	remoteDir := filepath.Join(configRoot, "remote.git")
	seedDir := filepath.Join(configRoot, "seed")
	runGitTest(t, "", "init", "--bare", remoteDir)
	runGitTest(t, "", "init", seedDir)
	runGitTest(t, seedDir, "config", "user.name", "GoDeploy Test")
	runGitTest(t, seedDir, "config", "user.email", "godeploy-test@example.test")
	runGitTest(t, seedDir, "checkout", "-b", "main")
	manifest := filepath.Join(seedDir, "package.json")
	if err := os.WriteFile(manifest, []byte(`{"name":"site"}`), 0o600); err != nil {
		t.Fatalf("write initial package manifest: %v", err)
	}
	runGitTest(t, seedDir, "add", "package.json")
	runGitTest(t, seedDir, "commit", "-m", "initial")
	runGitTest(t, seedDir, "remote", "add", "origin", remoteDir)
	runGitTest(t, seedDir, "push", "--set-upstream", "origin", "main")

	project := Project{Name: "site", RepositoryURL: remoteDir, Branch: "main"}
	if err := FetchRepositoryWithOutput(project, io.Discard); err != nil {
		t.Fatalf("initial FetchRepositoryWithOutput() error = %v", err)
	}

	if err := os.WriteFile(manifest, []byte(`{"name":"site","version":"2.0.0"}`), 0o600); err != nil {
		t.Fatalf("write updated package manifest: %v", err)
	}
	runGitTest(t, seedDir, "add", "package.json")
	runGitTest(t, seedDir, "commit", "-m", "update")
	runGitTest(t, seedDir, "push")

	if err := FetchRepositoryWithOutput(project, io.Discard); err != nil {
		t.Fatalf("updating FetchRepositoryWithOutput() error = %v", err)
	}
	checkoutManifest := filepath.Join(dataDir, project.Name, "package.json")
	data, err := os.ReadFile(checkoutManifest)
	if err != nil {
		t.Fatalf("read updated package manifest: %v", err)
	}
	if string(data) != `{"name":"site","version":"2.0.0"}` {
		t.Errorf("updated package manifest = %s, want version 2.0.0", data)
	}
}

func runGitTest(t *testing.T, workingDir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
