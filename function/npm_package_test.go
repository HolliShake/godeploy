package function

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

func TestNpmBuildCommandDefaultsToPackageBuildScript(t *testing.T) {
	tests := []struct {
		name              string
		buildCommand      string
		script            string
		prodBuildScript   string
		projectType       NpmProjectType
		wantCommand       string
		wantPackageScript bool
	}{
		{
			name:              "defaults to package build script",
			script:            "vite build",
			wantCommand:       "build",
			wantPackageScript: true,
		},
		{
			name:              "uses configured command",
			buildCommand:      "npm run generate",
			script:            "vite build",
			wantCommand:       "npm run generate",
			wantPackageScript: false,
		},
		{
			name:              "trims configured command",
			buildCommand:      "  npm run generate  ",
			script:            "vite build",
			wantCommand:       "npm run generate",
			wantPackageScript: false,
		},
		{
			name:              "prefers the Next.js production build script",
			script:            "next build",
			prodBuildScript:   "next build",
			projectType:       NextJS,
			wantCommand:       "prod-build",
			wantPackageScript: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := Project{
				BuildCommand: tt.buildCommand,
				NpmPackage: &NpmPackage{
					ProjectType: tt.projectType,
					Scripts: map[string]string{
						"build":      tt.script,
						"prod-build": tt.prodBuildScript,
					},
				},
			}
			command, usePackageScript := npmBuildCommand(project)
			if command != tt.wantCommand {
				t.Errorf("npmBuildCommand() command = %q, want %q", command, tt.wantCommand)
			}
			if usePackageScript != tt.wantPackageScript {
				t.Errorf("npmBuildCommand() usePackageScript = %t, want %t", usePackageScript, tt.wantPackageScript)
			}
		})
	}
}

func TestNormalizeWindowsBuildCommandSeparatesCommandsOutsideQuotes(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{
			name:    "POSIX semicolon sequence",
			command: "npm run orval ; npm run build",
			want:    "npm run orval && npm run build",
		},
		{
			name:    "preserves semicolon inside quoted argument",
			command: `node -e "console.log('a;b')" ; npm run build`,
			want:    `node -e "console.log('a;b')" && npm run build`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeWindowsBuildCommand(tt.command); got != tt.want {
				t.Errorf("normalizeWindowsBuildCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindBuildOutputDirFindsConventionalOutput(t *testing.T) {
	projectPath := t.TempDir()
	want := filepath.Join(projectPath, ".next")
	if err := os.Mkdir(want, 0o755); err != nil {
		t.Fatalf("create conventional output directory: %v", err)
	}

	got, err := findBuildOutputDir(projectPath, NextJS)
	if err != nil {
		t.Fatalf("findBuildOutputDir() error = %v", err)
	}
	if got != want {
		t.Errorf("findBuildOutputDir() = %q, want %q", got, want)
	}
}

func TestValidateNextProductionBuildRequiresBuildID(t *testing.T) {
	projectPath := t.TempDir()
	nextBuildDir := filepath.Join(projectPath, ".next")
	if err := os.MkdirAll(nextBuildDir, 0o755); err != nil {
		t.Fatalf("create Next.js build directory: %v", err)
	}
	if err := validateNextProductionBuild(projectPath); err == nil {
		t.Fatal("validateNextProductionBuild() error = nil, want missing BUILD_ID error")
	}

	buildIDPath := filepath.Join(nextBuildDir, "BUILD_ID")
	if err := os.WriteFile(buildIDPath, []byte("build-id"), 0o600); err != nil {
		t.Fatalf("write Next.js build ID: %v", err)
	}
	if err := validateNextProductionBuild(projectPath); err != nil {
		t.Fatalf("validateNextProductionBuild() error = %v", err)
	}
}

func TestStandaloneConfigDetection(t *testing.T) {
	tests := []struct {
		name        string
		projectType NpmProjectType
		fileName    string
		config      string
		want        bool
	}{
		{
			name:        "Next standalone setting in conditional object",
			projectType: NextJS,
			fileName:    "next.config.mjs",
			config:      `const config = { ...(process.env.VERCEL ? {} : { output: "standalone" }) }; export default config;`,
			want:        true,
		},
		{
			name:        "ignores commented Next setting",
			projectType: NextJS,
			fileName:    "next.config.js",
			config:      "// output: 'standalone'\nmodule.exports = {};",
		},
		{
			name:        "Nuxt node server preset",
			projectType: NuxtJS,
			fileName:    "nuxt.config.ts",
			config:      `export default defineNuxtConfig({ nitro: { preset: "node-server" } })`,
			want:        true,
		},
		{
			name:        "Nuxt preset without Nitro configuration",
			projectType: NuxtJS,
			fileName:    "nuxt.config.js",
			config:      `const config = { preset: "node-server" }; export default config;`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectPath := t.TempDir()
			configPath := filepath.Join(projectPath, tt.fileName)
			if err := os.WriteFile(configPath, []byte(tt.config), 0o600); err != nil {
				t.Fatalf("write framework config: %v", err)
			}
			got, err := standaloneConfigEnabled(tt.projectType, projectPath)
			if err != nil {
				t.Fatalf("standaloneConfigEnabled() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("standaloneConfigEnabled() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestPackageManagerForProjectUsesBunLockfile(t *testing.T) {
	projectPath := t.TempDir()
	if got := packageManagerForProject(projectPath); got != "npm" {
		t.Fatalf("packageManagerForProject() = %q, want npm without a Bun lockfile", got)
	}
	for _, name := range []string{"bun.lock", "bun.lockb"} {
		if err := os.WriteFile(filepath.Join(projectPath, name), nil, 0o600); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if got := packageManagerForProject(projectPath); got != "bun" {
			t.Errorf("packageManagerForProject() with %s = %q, want bun", name, got)
		}
		if err := os.Remove(filepath.Join(projectPath, name)); err != nil {
			t.Fatalf("remove %s: %v", name, err)
		}
	}
}

func TestStageNextStandaloneBundlesRuntimeAssets(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("APPDATA", configRoot)
	projectPath := filepath.Join(configRoot, AppName, "site")
	files := map[string]string{
		filepath.Join(projectPath, ".next", "standalone", "server.js"):     "server",
		filepath.Join(projectPath, "public", "images", "logo.svg"):         "logo",
		filepath.Join(projectPath, ".next", "static", "chunks", "main.js"): "chunk",
		filepath.Join(projectPath, ".env"):                                 "SOURCE_ENV=from-file\n",
	}
	for path, contents := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create directory for %q: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write %q: %v", path, err)
		}
	}

	project := Project{
		Name: "site",
		NpmPackage: &NpmPackage{
			ProjectType: NextJS,
			PackagePath: filepath.Join(projectPath, "package.json"),
		},
		EnvironmentVariables: map[string]string{"PORT": "3001"},
	}
	deploymentDir := nextStandaloneDeploymentDir(project)
	var output bytes.Buffer
	if err := stageNextStandalone(project, projectPath, &output); err != nil {
		t.Fatalf("stageNextStandalone() error = %v", err)
	}

	copies := map[string]string{
		filepath.Join(deploymentDir, "server.js"):                            "server",
		filepath.Join(deploymentDir, "public", "images", "logo.svg"):         "logo",
		filepath.Join(deploymentDir, ".next", "static", "chunks", "main.js"): "chunk",
	}
	for path, want := range copies {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read bundled asset %q: %v", path, err)
			continue
		}
		if string(got) != want {
			t.Errorf("bundled asset %q = %q, want %q", path, got, want)
		}
	}
	envFile, err := os.Open(filepath.Join(deploymentDir, ".env"))
	if err != nil {
		t.Fatalf("open staged environment file: %v", err)
	}
	env, err := ParseEnvironmentFile(envFile)
	if closeErr := envFile.Close(); closeErr != nil {
		t.Fatalf("close staged environment file: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("parse staged environment file: %v", err)
	}
	if env["PORT"] != "3001" || env["SOURCE_ENV"] != "from-file" || env["NODE_ENV"] != "production" {
		t.Errorf("staged runtime environment = %#v", env)
	}
	if !strings.Contains(output.String(), "Bundled Next.js assets:") || !strings.Contains(output.String(), "ecosystem.config.js") {
		t.Errorf("output %q does not report bundled assets", output.String())
	}

	if got := nextStandaloneDir(project); got != deploymentDir {
		t.Errorf("nextStandaloneDir() = %q, want standalone directory", got)
	}
}

func TestStageNextStandaloneRequiresConfiguredOutput(t *testing.T) {
	projectPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectPath, "next.config.mjs"), []byte(`export default { output: "standalone" }`), 0o600); err != nil {
		t.Fatalf("write Next.js config: %v", err)
	}
	project := Project{
		Name: "site",
		NpmPackage: &NpmPackage{
			ProjectType: NextJS,
			PackagePath: filepath.Join(projectPath, "package.json"),
		},
	}
	if err := stageNextStandalone(project, projectPath, &bytes.Buffer{}); err == nil {
		t.Fatal("stageNextStandalone() error = nil, want missing standalone server error")
	}
}

func TestStageNuxtStandaloneOutput(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("APPDATA", configRoot)
	projectPath := filepath.Join(configRoot, AppName, "site")
	if err := os.MkdirAll(filepath.Join(projectPath, ".output", "server"), 0o755); err != nil {
		t.Fatalf("create Nuxt server directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, "nuxt.config.ts"), []byte(`export default defineNuxtConfig({ nitro: { preset: "node-server" } })`), 0o600); err != nil {
		t.Fatalf("write Nuxt config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, ".output", "server", "index.mjs"), []byte("server"), 0o600); err != nil {
		t.Fatalf("write Nuxt server entry: %v", err)
	}

	project := Project{
		Name:       "site",
		NpmPackage: &NpmPackage{ProjectType: NuxtJS, PackagePath: filepath.Join(projectPath, "package.json")},
		EnvironmentVariables: map[string]string{
			"PORT": "3001",
		},
	}
	deploymentDir := nextStandaloneDeploymentDir(project)
	var output bytes.Buffer
	if err := stageNuxtStandalone(project, projectPath, &output); err != nil {
		t.Fatalf("stageNuxtStandalone() error = %v", err)
	}
	if !fileExists(filepath.Join(deploymentDir, "server", "index.mjs")) {
		t.Fatal("staged Nuxt server entry is missing")
	}
	config, err := os.ReadFile(filepath.Join(deploymentDir, "ecosystem.config.js"))
	if err != nil {
		t.Fatalf("read generated PM2 config: %v", err)
	}
	if !strings.Contains(string(config), "server/index.mjs") && !strings.Contains(string(config), `server\\index.mjs`) {
		t.Errorf("generated PM2 config does not target Nuxt server entry: %s", config)
	}
	if !strings.Contains(string(config), `"PORT": "3001"`) || !strings.Contains(string(config), `"NODE_ENV": "production"`) {
		t.Errorf("generated PM2 config missing runtime environment: %s", config)
	}
	if got := nuxtStandaloneDir(project); got != deploymentDir {
		t.Errorf("nuxtStandaloneDir() = %q, want standalone directory", got)
	}
}
