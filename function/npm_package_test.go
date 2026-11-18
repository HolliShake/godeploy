package function

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectNpmPackageFindsManifestRecursively(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("APPDATA", configRoot)

	projectPath := filepath.Join(configRoot, AppName, "site")
	nestedManifest := filepath.Join(projectPath, "frontend", "package.json")
	dependencyManifest := filepath.Join(projectPath, "node_modules", "dependency", "package.json")
	for path, content := range map[string]string{
		nestedManifest:     `{"name":"site-frontend","version":"1.0.0"}`,
		dependencyManifest: `{"name":"dependency","version":"1.0.0"}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create package directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write package manifest: %v", err)
		}
	}

	pkg, err := DetectNpmPackage(Project{Name: "site"})
	if err != nil {
		t.Fatalf("DetectNpmPackage() error = %v", err)
	}
	if pkg.Name != "site-frontend" {
		t.Errorf("package name = %q, want %q", pkg.Name, "site-frontend")
	}
	if pkg.PackagePath != nestedManifest {
		t.Errorf("package path = %q, want %q", pkg.PackagePath, nestedManifest)
	}
}

func TestDetectNpmPackagePrefersProjectRootManifest(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("APPDATA", configRoot)

	projectPath := filepath.Join(configRoot, AppName, "site")
	rootManifest := filepath.Join(projectPath, "package.json")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	if err := os.WriteFile(rootManifest, []byte(`{"name":"site-root"}`), 0o600); err != nil {
		t.Fatalf("write package manifest: %v", err)
	}

	pkg, err := DetectNpmPackage(Project{Name: "site"})
	if err != nil {
		t.Fatalf("DetectNpmPackage() error = %v", err)
	}
	if pkg.Name != "site-root" {
		t.Errorf("package name = %q, want %q", pkg.Name, "site-root")
	}
	if pkg.PackagePath != rootManifest {
		t.Errorf("package path = %q, want %q", pkg.PackagePath, rootManifest)
	}
}

func TestDetectNpmPackageProjectType(t *testing.T) {
	tests := []struct {
		name        string
		manifest    string
		projectType NpmProjectType
	}{
		{
			name:        "Next.js",
			manifest:    `{"dependencies":{"next":"1.0.0"}}`,
			projectType: NextJS,
		},
		{
			name:        "Nuxt",
			manifest:    `{"devDependencies":{"nuxt":"1.0.0"}}`,
			projectType: NuxtJS,
		},
		{
			name:        "SvelteKit takes precedence over Vite",
			manifest:    `{"dependencies":{"vite":"1.0.0","@sveltejs/kit":"1.0.0"}}`,
			projectType: SvelteKit,
		},
		{
			name:        "Vue CLI",
			manifest:    `{"dependencies":{"@vue/cli-service":"1.0.0"}}`,
			projectType: VueCLI,
		},
		{
			name:        "Vite",
			manifest:    `{"devDependencies":{"vite":"1.0.0"}}`,
			projectType: Vite,
		},
		{
			name:        "plain web project",
			manifest:    `{"dependencies":{"unrecognized":"1.0.0"}}`,
			projectType: Vanilla,
		},
	}

	configRoot := t.TempDir()
	t.Setenv("APPDATA", configRoot)
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectName := filepath.Join("project", string(rune('a'+i)))
			projectPath := filepath.Join(configRoot, AppName, projectName)
			manifestPath := filepath.Join(projectPath, "package.json")
			if err := os.MkdirAll(projectPath, 0o755); err != nil {
				t.Fatalf("create project directory: %v", err)
			}
			if err := os.WriteFile(manifestPath, []byte(tt.manifest), 0o600); err != nil {
				t.Fatalf("write package manifest: %v", err)
			}

			pkg, err := DetectNpmPackage(Project{Name: projectName})
			if err != nil {
				t.Fatalf("DetectNpmPackage() error = %v", err)
			}
			if pkg.ProjectType != tt.projectType {
				t.Errorf("project type = %q, want %q", pkg.ProjectType, tt.projectType)
			}
		})
	}
}
