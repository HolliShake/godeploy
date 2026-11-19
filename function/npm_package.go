package function

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type NpmPackageType string

type NpmProjectType string

const (
	Module   NpmPackageType = "module"
	CommonJS NpmPackageType = "commonjs" // Default
)

const (
	NextJS    NpmProjectType = "nextjs"
	NuxtJS    NpmProjectType = "nuxtjs"
	SvelteKit NpmProjectType = "sveltekit"
	VueCLI    NpmProjectType = "vue-cli"
	Vite      NpmProjectType = "vite"
	Vanilla   NpmProjectType = "vanilla"
)

type NpmPackage struct {
	Name        string
	Version     string
	Type        NpmPackageType
	ProjectType NpmProjectType
	Scripts     map[string]string
	PackagePath string
}

// npmManifest mirrors the fields of package.json we care about.
type npmManifest struct {
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Type            string            `json:"type"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func (m *npmManifest) hasDependency(name string) bool {
	if _, ok := m.Dependencies[name]; ok {
		return true
	}
	_, ok := m.DevDependencies[name]
	return ok
}

// projectRule describes how to recognise one framework. Order matters:
// the first matching rule wins (e.g. Next.js projects often also ship Vite for tests).
type projectRule struct {
	projectType NpmProjectType
	dependency  string   // package name in dependencies / devDependencies
	command     string   // CLI binary used in scripts (fallback signal)
	configFiles []string // config files in the project root (fallback signal)
}

var projectRules = []projectRule{
	{NextJS, "next", "next", []string{"next.config.js", "next.config.mjs", "next.config.cjs", "next.config.ts"}},
	{NuxtJS, "nuxt", "nuxt", []string{"nuxt.config.js", "nuxt.config.mjs", "nuxt.config.ts"}},
	{SvelteKit, "@sveltejs/kit", "svelte-kit", []string{"svelte.config.js", "svelte.config.ts"}},
	{VueCLI, "@vue/cli-service", "vue-cli-service", []string{"vue.config.js", "vue.config.cjs"}},
	{Vite, "vite", "vite", []string{"vite.config.js", "vite.config.mjs", "vite.config.cjs", "vite.config.ts"}},
}

// Directories never searched for a project manifest. Dot-directories are skipped too.
var skippedDirs = map[string]struct{}{
	"node_modules": {},
	"dist":         {},
	"build":        {},
	"coverage":     {},
	"out":          {},
}

func DetectNpmPackage(project Project) (*NpmPackage, error) {
	configPath := GetConfigPath()
	projectPath := filepath.Join(filepath.Dir(configPath), project.Name)
	packagePath, err := findNpmPackageManifest(projectPath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(packagePath)
	if err != nil {
		return nil, fmt.Errorf("read npm package manifest %q: %w", packagePath, err)
	}

	var manifest npmManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse npm package manifest %q: %w", packagePath, err)
	}

	packageType := CommonJS
	switch manifest.Type {
	case "", string(CommonJS):
	case string(Module):
		packageType = Module
	default:
		return nil, fmt.Errorf("unsupported npm package type %q in %q", manifest.Type, packagePath)
	}

	if manifest.Scripts == nil {
		manifest.Scripts = map[string]string{}
	}

	return &NpmPackage{
		Name:        manifest.Name,
		Version:     manifest.Version,
		Type:        packageType,
		ProjectType: detectNpmProjectType(&manifest, filepath.Dir(packagePath)),
		Scripts:     manifest.Scripts,
		PackagePath: packagePath,
	}, nil
}

// detectNpmProjectType uses three signals, strongest first:
//  1. dependencies / devDependencies
//  2. framework config files next to package.json
//  3. CLI commands used in package.json scripts (e.g. "next build")
func detectNpmProjectType(manifest *npmManifest, dir string) NpmProjectType {
	for _, rule := range projectRules {
		if manifest.hasDependency(rule.dependency) {
			return rule.projectType
		}
	}
	for _, rule := range projectRules {
		for _, name := range rule.configFiles {
			if fileExists(filepath.Join(dir, name)) {
				return rule.projectType
			}
		}
	}
	for _, rule := range projectRules {
		for _, script := range manifest.Scripts {
			if scriptUsesCommand(script, rule.command) {
				return rule.projectType
			}
		}
	}
	return Vanilla
}

func scriptUsesCommand(script, command string) bool {
	for _, token := range strings.Fields(script) {
		if token == command {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// findNpmPackageManifest returns the shallowest package.json under projectPath.
// It walks breadth-first, so the root manifest is found immediately and
// build artefacts (.next/standalone, dist, ...) can never shadow the real one.
// Directories are read lazily, so large trees are not traversed unnecessarily.
func findNpmPackageManifest(projectPath string) (string, error) {
	info, err := os.Stat(projectPath)
	if err != nil {
		return "", fmt.Errorf("check project path %q: %w", projectPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project path %q is not a directory", projectPath)
	}

	queue := []string{projectPath}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]

		entries, err := os.ReadDir(dir) // sorted by name -> deterministic
		if err != nil {
			if dir == projectPath {
				return "", fmt.Errorf("search npm package manifests under %q: %w", projectPath, err)
			}
			continue // unreadable subdirectory: ignore
		}

		var subdirs []string
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() {
				if _, skip := skippedDirs[name]; skip || strings.HasPrefix(name, ".") {
					continue
				}
				subdirs = append(subdirs, filepath.Join(dir, name))
				continue
			}
			if name == "package.json" {
				return filepath.Join(dir, name), nil
			}
		}
		queue = append(queue, subdirs...)
	}

	return "", fmt.Errorf("npm package manifest not found under %q", projectPath)
}

var npmInstallArgs = []string{"install", "--foreground-scripts", "--no-progress", "--loglevel=info"}

func NpmInstallWithOutput(project Project, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}

	packagePath := npmPackageManifestPath(project)
	if _, err := os.Stat(packagePath); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("npm package manifest not found at %q", packagePath)
	} else if err != nil {
		return fmt.Errorf("check npm package manifest %q: %w", packagePath, err)
	}

	dir := filepath.Dir(packagePath)
	_, _ = fmt.Fprintf(output, "$ npm %s\n", strings.Join(npmInstallArgs, " "))
	if err := runNpmCommand(dir, output, npmInstallArgs...); err != nil {
		_, _ = fmt.Fprintf(output, "\nInitial npm install failed: %v\nRetrying with --legacy-peer-deps...\n", err)

		retryArgs := append(append([]string(nil), npmInstallArgs...), "--legacy-peer-deps")
		_, _ = fmt.Fprintf(output, "$ npm %s\n", strings.Join(retryArgs, " "))
		if retryErr := runNpmCommand(dir, output, retryArgs...); retryErr != nil {
			return fmt.Errorf("npm install failed: %v; retry with --legacy-peer-deps failed: %w", err, retryErr)
		}
	}
	_, _ = fmt.Fprintln(output, "npm install completed.")
	return nil
}

func NpmBuildWithOutput(project Project, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}

	if project.NpmPackage == nil {
		_, _ = fmt.Fprintln(output, "No npm package detected; skipping build.")
		return nil
	}
	buildCommand, usePackageBuildScript := npmBuildCommand(project)
	if buildCommand == "" {
		_, _ = fmt.Fprintln(output, "No build command configured; skipping npm build.")
		return nil
	}

	projectPath := filepath.Dir(npmPackageManifestPath(project))
	if usePackageBuildScript {
		args := []string{"--foreground-scripts", "--no-progress", "--loglevel=info", "run", "build"}
		_, _ = fmt.Fprintf(output, "$ npm %s\n", strings.Join(args, " "))
		_, _ = fmt.Fprintf(output, "> %s\n", buildCommand)
		if err := runNpmCommand(projectPath, output, args...); err != nil {
			return fmt.Errorf("run npm build in %q: %w", projectPath, err)
		}
	} else {
		_, _ = fmt.Fprintf(output, "$ %s\n", buildCommand)
		if err := runBuildCommand(projectPath, output, buildCommand); err != nil {
			return fmt.Errorf("run build command in %q: %w", projectPath, err)
		}
	}

	outDir, err := findBuildOutputDir(projectPath, project.NpmPackage.ProjectType)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(output, "npm build completed. Output directory: %s\n", outDir)
	return nil
}

func npmBuildCommand(project Project) (string, bool) {
	if command := strings.TrimSpace(project.BuildCommand); command != "" {
		return command, false
	}
	return strings.TrimSpace(project.NpmPackage.Scripts["build"]), true
}

// npmBuildOutputCandidates lists the directories each framework may emit,
// in order of preference (e.g. Next.js: .next, or out/dist with static export).
func npmBuildOutputCandidates(projectType NpmProjectType) []string {
	switch projectType {
	case NextJS:
		return []string{".next", "out", "dist"}
	case NuxtJS:
		return []string{".output", "dist"}
	case SvelteKit:
		return []string{"build", ".svelte-kit", "dist"}
	default: // VueCLI, Vite, Vanilla
		return []string{"dist", "build"}
	}
}

func findBuildOutputDir(projectPath string, projectType NpmProjectType) (string, error) {
	candidates := npmBuildOutputCandidates(projectType)
	for _, name := range candidates {
		path := filepath.Join(projectPath, name)
		info, err := os.Stat(path)
		switch {
		case err == nil && info.IsDir():
			return path, nil
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("check npm build output directory %q: %w", path, err)
		}
	}
	return "", fmt.Errorf("npm build completed but none of the expected output directories (%s) exist in %q",
		strings.Join(candidates, ", "), projectPath)
}

func npmPackageManifestPath(project Project) string {
	if project.NpmPackage != nil && project.NpmPackage.PackagePath != "" {
		return project.NpmPackage.PackagePath
	}
	return filepath.Join(filepath.Dir(GetConfigPath()), project.Name, "package.json")
}

func runNpmCommand(workingDir string, output io.Writer, args ...string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", append([]string{"/c", "npm"}, args...)...)
	} else {
		cmd = exec.Command("npm", args...)
	}
	cmd.Dir = workingDir

	// Using the same writer for both streams makes os/exec copy them through a
	// single goroutine, so output is serialized and interleaved in order, with
	// no manual pipes, WaitGroup or data races.
	cmd.Stdout = output
	cmd.Stderr = output

	return cmd.Run()
}

func runBuildCommand(workingDir string, output io.Writer, command string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	cmd.Dir = workingDir
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd.Run()
}
