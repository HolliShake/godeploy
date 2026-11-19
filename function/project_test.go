package function

import (
	"os"
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
