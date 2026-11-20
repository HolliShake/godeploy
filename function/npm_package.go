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
	{NuxtJS, "nuxt", "nuxt", []string{"nuxt.config.js", "nuxt.config.mjs", "nuxt.config.cjs", "nuxt.config.ts"}},
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

func standaloneConfigEnabled(projectType NpmProjectType, projectPath string) (bool, error) {
	var configFiles []string
	for _, rule := range projectRules {
		if rule.projectType == projectType {
			configFiles = rule.configFiles
			break
		}
	}
	for _, name := range configFiles {
		path := filepath.Join(projectPath, name)
		config, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("read framework config %q: %w", path, err)
		}
		switch projectType {
		case NextJS:
			return jsConfigHasProperty(config, "output", "standalone"), nil
		case NuxtJS:
			return jsConfigHasProperty(config, "nitro", "") &&
				jsConfigHasProperty(config, "preset", "node-server"), nil
		default:
			return false, nil
		}
	}
	return false, nil
}

func jsConfigHasProperty(source []byte, name, expectedValue string) bool {
	for i := 0; i < len(source); {
		i = skipJSWhitespaceAndComments(source, i)
		if i >= len(source) {
			return false
		}

		key := ""
		next := i
		switch source[i] {
		case '\'', '"', '`':
			var ok bool
			key, next, ok = readJSStringLiteral(source, i)
			if !ok {
				return false
			}
		default:
			if !isJSIdentifierStart(source[i]) {
				i++
				continue
			}
			next++
			for next < len(source) && isJSIdentifierPart(source[next]) {
				next++
			}
			key = string(source[i:next])
		}

		if key == name {
			colon := skipJSWhitespaceAndComments(source, next)
			if colon < len(source) && source[colon] == ':' {
				if expectedValue == "" {
					valueIndex := skipJSWhitespaceAndComments(source, colon+1)
					return valueIndex < len(source) && source[valueIndex] == '{'
				}
				valueIndex := skipJSWhitespaceAndComments(source, colon+1)
				if valueIndex < len(source) && (source[valueIndex] == '\'' || source[valueIndex] == '"') {
					value, _, ok := readJSStringLiteral(source, valueIndex)
					if ok && value == expectedValue {
						return true
					}
				}
			}
		}
		i = next
	}
	return false
}

func skipJSWhitespaceAndComments(source []byte, index int) int {
	for index < len(source) {
		switch {
		case source[index] == ' ' || source[index] == '\t' || source[index] == '\r' || source[index] == '\n':
			index++
		case index+1 < len(source) && source[index] == '/' && source[index+1] == '/':
			index += 2
			for index < len(source) && source[index] != '\n' {
				index++
			}
		case index+1 < len(source) && source[index] == '/' && source[index+1] == '*':
			index += 2
			for index+1 < len(source) && !(source[index] == '*' && source[index+1] == '/') {
				index++
			}
			if index+1 < len(source) {
				index += 2
			}
		default:
			return index
		}
	}
	return index
}

func readJSStringLiteral(source []byte, index int) (string, int, bool) {
	quote := source[index]
	if quote != '\'' && quote != '"' && quote != '`' {
		return "", index, false
	}
	var value strings.Builder
	for i := index + 1; i < len(source); i++ {
		switch source[i] {
		case '\\':
			if i+1 >= len(source) {
				return "", len(source), false
			}
			i++
			value.WriteByte(source[i])
		case quote:
			return value.String(), i + 1, true
		default:
			value.WriteByte(source[i])
		}
	}
	return "", len(source), false
}

func isJSIdentifierStart(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char == '_' || char == '$'
}

func isJSIdentifierPart(char byte) bool {
	return isJSIdentifierStart(char) || char >= '0' && char <= '9'
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
	manager := packageManagerForProject(dir)
	installArgs := npmInstallArgs
	if manager == "bun" {
		installArgs = []string{"install", "--frozen-lockfile"}
	}
	buildEnv := projectBuildEnvironment(project)
	_, _ = fmt.Fprintf(output, "$ %s %s\n", manager, strings.Join(installArgs, " "))
	if err := runPackageManagerCommand(manager, dir, output, buildEnv, installArgs...); err != nil {
		if manager == "bun" {
			return fmt.Errorf("bun install failed: %w", err)
		}
		_, _ = fmt.Fprintf(output, "\nInitial npm install failed: %v\nRetrying with --legacy-peer-deps...\n", err)

		retryArgs := append(append([]string(nil), installArgs...), "--legacy-peer-deps")
		_, _ = fmt.Fprintf(output, "$ npm %s\n", strings.Join(retryArgs, " "))
		if retryErr := runPackageManagerCommand(manager, dir, output, buildEnv, retryArgs...); retryErr != nil {
			return fmt.Errorf("npm install failed: %v; retry with --legacy-peer-deps failed: %w", err, retryErr)
		}
	}
	_, _ = fmt.Fprintf(output, "%s install completed.\n", manager)
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
	buildEnv := projectBuildEnvironment(project)
	if usePackageBuildScript {
		manager := packageManagerForProject(projectPath)
		args := []string{"run", buildCommand}
		if manager == "npm" {
			args = []string{"--foreground-scripts", "--no-progress", "--loglevel=info", "run", buildCommand}
		}
		_, _ = fmt.Fprintf(output, "$ %s %s\n", manager, strings.Join(args, " "))
		_, _ = fmt.Fprintf(output, "> %s\n", project.NpmPackage.Scripts[buildCommand])
		if err := runPackageManagerCommand(manager, projectPath, output, buildEnv, args...); err != nil {
			return fmt.Errorf("run %s %s in %q: %w", manager, buildCommand, projectPath, err)
		}
	} else {
		_, _ = fmt.Fprintf(output, "$ %s\n", buildCommand)
		if err := runBuildCommandWithEnv(projectPath, output, buildEnv, buildCommand); err != nil {
			return fmt.Errorf("run build command in %q: %w", projectPath, err)
		}
	}

	outDir, err := findBuildOutputDir(projectPath, project.NpmPackage.ProjectType)
	if err != nil {
		return err
	}

	switch project.NpmPackage.ProjectType {
	case NextJS:
		if err := stageNextStandalone(project, projectPath, output); err != nil {
			return err
		}
	case NuxtJS:
		if err := stageNuxtStandalone(project, projectPath, output); err != nil {
			return err
		}
	}

	_, _ = fmt.Fprintf(output, "npm build completed. Output directory: %s\n", outDir)
	return nil
}

func npmBuildCommand(project Project) (string, bool) {
	if command := strings.TrimSpace(project.BuildCommand); command != "" {
		return command, false
	}
	if project.NpmPackage.ProjectType == NextJS {
		if script := strings.TrimSpace(project.NpmPackage.Scripts["prod-build"]); script != "" {
			return "prod-build", true
		}
	}
	if strings.TrimSpace(project.NpmPackage.Scripts["build"]) != "" {
		return "build", true
	}
	return "", true
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

func validateNextProductionBuild(projectPath string) error {
	buildIDPath := filepath.Join(projectPath, ".next", "BUILD_ID")
	info, err := os.Stat(buildIDPath)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("Next.js production build is missing %q; build the app before starting PM2", buildIDPath)
	}
	if err != nil {
		return fmt.Errorf("check Next.js production build %q: %w", buildIDPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Next.js production build ID %q is not a regular file", buildIDPath)
	}
	return nil
}

func stageNextStandalone(project Project, projectPath string, output io.Writer) error {
	sourceDir := filepath.Join(projectPath, ".next", "standalone")
	configured, err := standaloneConfigEnabled(NextJS, projectPath)
	if err != nil {
		return err
	}
	if !fileExists(filepath.Join(sourceDir, "server.js")) {
		if configured {
			return fmt.Errorf("Next.js config enables standalone output, but build did not produce %q", filepath.Join(sourceDir, "server.js"))
		}
		return nil
	}
	if configured {
		_, _ = fmt.Fprintln(output, "Detected standalone output in Next.js config.")
	}
	if err := validateProjectName(project.Name); err != nil {
		return fmt.Errorf("invalid project name for standalone deployment: %w", err)
	}

	deploymentDir := nextStandaloneDeploymentDir(project)
	if err := os.MkdirAll(filepath.Dir(deploymentDir), 0o755); err != nil {
		return fmt.Errorf("create Next.js deployment directory: %w", err)
	}
	if err := os.RemoveAll(deploymentDir); err != nil {
		return fmt.Errorf("remove previous Next.js deployment directory %q: %w", deploymentDir, err)
	}
	if err := copyDirectory(sourceDir, deploymentDir); err != nil {
		return fmt.Errorf("copy Next.js standalone output from %q to %q: %w", sourceDir, deploymentDir, err)
	}
	_, _ = fmt.Fprintf(output, "Copied Next.js standalone output: %s -> %s\n", sourceDir, deploymentDir)

	assets := []struct {
		source      string
		destination string
	}{
		{filepath.Join(projectPath, "public"), filepath.Join(deploymentDir, "public")},
		{filepath.Join(projectPath, ".next", "static"), filepath.Join(deploymentDir, ".next", "static")},
	}
	for _, asset := range assets {
		info, err := os.Stat(asset.source)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("check Next.js standalone asset directory %q: %w", asset.source, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("Next.js standalone asset path %q is not a directory", asset.source)
		}
		if err := copyDirectory(asset.source, asset.destination); err != nil {
			return fmt.Errorf("copy Next.js standalone assets from %q to %q: %w", asset.source, asset.destination, err)
		}
		_, _ = fmt.Fprintf(output, "Bundled Next.js assets: %s -> %s\n", asset.source, asset.destination)
	}

	runtimeEnv, err := nextRuntimeEnvironment(project, projectPath)
	if err != nil {
		return err
	}
	if err := WriteEnvironmentFile(filepath.Join(deploymentDir, ".env"), runtimeEnv); err != nil {
		return fmt.Errorf("write Next.js runtime environment: %w", err)
	}
	if err := writeNextEcosystemConfig(project.Name, deploymentDir, runtimeEnv); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(output, "Generated PM2 ecosystem config: %s\n", filepath.Join(deploymentDir, "ecosystem.config.js"))
	_, _ = fmt.Fprintf(output, "Staged Next.js standalone application at %s\n", deploymentDir)
	return nil
}

func stageNuxtStandalone(project Project, projectPath string, output io.Writer) error {
	sourceDir := filepath.Join(projectPath, ".output")
	serverPath := filepath.Join(sourceDir, "server", "index.mjs")
	configured, err := standaloneConfigEnabled(NuxtJS, projectPath)
	if err != nil {
		return err
	}
	if !fileExists(serverPath) {
		if configured {
			return fmt.Errorf("Nuxt config selects the node-server preset, but build did not produce %q", serverPath)
		}
		return nil
	}
	if configured {
		_, _ = fmt.Fprintln(output, "Detected standalone node-server preset in Nuxt config.")
	}
	if err := validateProjectName(project.Name); err != nil {
		return fmt.Errorf("invalid project name for standalone deployment: %w", err)
	}

	deploymentDir := nextStandaloneDeploymentDir(project)
	if err := os.MkdirAll(filepath.Dir(deploymentDir), 0o755); err != nil {
		return fmt.Errorf("create Nuxt deployment directory: %w", err)
	}
	if err := os.RemoveAll(deploymentDir); err != nil {
		return fmt.Errorf("remove previous Nuxt deployment directory %q: %w", deploymentDir, err)
	}
	if err := copyDirectory(sourceDir, deploymentDir); err != nil {
		return fmt.Errorf("copy Nuxt output from %q to %q: %w", sourceDir, deploymentDir, err)
	}

	runtimeEnv, err := standaloneRuntimeEnvironment(project, projectPath, false)
	if err != nil {
		return err
	}
	if err := WriteEnvironmentFile(filepath.Join(deploymentDir, ".env"), runtimeEnv); err != nil {
		return fmt.Errorf("write Nuxt runtime environment: %w", err)
	}
	if err := writeStandaloneEcosystemConfig(project.Name, deploymentDir, filepath.Join("server", "index.mjs"), runtimeEnv); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(output, "Staged Nuxt standalone application at %s\n", deploymentDir)
	return nil
}

func nextRuntimeEnvironment(project Project, projectPath string) (map[string]string, error) {
	return standaloneRuntimeEnvironment(project, projectPath, true)
}

func standaloneRuntimeEnvironment(project Project, projectPath string, next bool) (map[string]string, error) {
	runtimeEnv := make(map[string]string)
	envPath := filepath.Join(projectPath, ".env")
	if info, err := os.Stat(envPath); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("Next.js environment file %q is not a regular file", envPath)
		}
		file, err := os.Open(envPath)
		if err != nil {
			return nil, fmt.Errorf("open Next.js environment file %q: %w", envPath, err)
		}
		vars, parseErr := ParseEnvironmentFile(file)
		closeErr := file.Close()
		if parseErr != nil {
			return nil, fmt.Errorf("parse Next.js environment file %q: %w", envPath, parseErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close Next.js environment file %q: %w", envPath, closeErr)
		}
		for key, value := range vars {
			runtimeEnv[key] = value
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("check Next.js environment file %q: %w", envPath, err)
	}
	for key, value := range project.EnvironmentVariables {
		runtimeEnv[key] = value
	}
	runtimeEnv["NODE_ENV"] = "production"
	if next {
		if _, ok := runtimeEnv["AUTH_TRUST_HOST"]; !ok {
			runtimeEnv["AUTH_TRUST_HOST"] = "true"
		}
		if _, ok := runtimeEnv["NEXTAUTH_TRUST_HOST"]; !ok {
			runtimeEnv["NEXTAUTH_TRUST_HOST"] = "true"
		}
	}
	return runtimeEnv, nil
}

func writeNextEcosystemConfig(projectName, deploymentDir string, runtimeEnv map[string]string) error {
	return writeStandaloneEcosystemConfig(projectName, deploymentDir, "server.js", runtimeEnv)
}

func writeStandaloneEcosystemConfig(projectName, deploymentDir, serverRelativePath string, runtimeEnv map[string]string) error {
	scriptPath, err := filepath.Abs(filepath.Join(deploymentDir, serverRelativePath))
	if err != nil {
		return fmt.Errorf("resolve Next.js server path: %w", err)
	}
	workingDir, err := filepath.Abs(deploymentDir)
	if err != nil {
		return fmt.Errorf("resolve Next.js deployment directory: %w", err)
	}
	config := struct {
		Apps []struct {
			Name   string            `json:"name"`
			Script string            `json:"script"`
			Cwd    string            `json:"cwd"`
			Env    map[string]string `json:"env"`
		} `json:"apps"`
	}{}
	config.Apps = append(config.Apps, struct {
		Name   string            `json:"name"`
		Script string            `json:"script"`
		Cwd    string            `json:"cwd"`
		Env    map[string]string `json:"env"`
	}{
		Name: projectName, Script: scriptPath, Cwd: workingDir, Env: runtimeEnv,
	})
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Next.js PM2 configuration: %w", err)
	}
	configPath := filepath.Join(deploymentDir, "ecosystem.config.js")
	contents := append([]byte("module.exports = "), data...)
	contents = append(contents, ';', '\n')
	if err := os.WriteFile(configPath, contents, 0o600); err != nil {
		return fmt.Errorf("write Next.js PM2 configuration %q: %w", configPath, err)
	}
	if err := os.Chmod(configPath, 0o600); err != nil {
		return fmt.Errorf("set Next.js PM2 configuration permissions: %w", err)
	}
	return nil
}

func copyDirectory(source, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}

	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(source, entry.Name())
		destinationPath := filepath.Join(destination, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("read file info for %q: %w", sourcePath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic link %q cannot be bundled", sourcePath)
		}
		if entry.IsDir() {
			if err := copyDirectory(sourcePath, destinationPath); err != nil {
				return fmt.Errorf("copy directory %q: %w", sourcePath, err)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported asset type at %q", sourcePath)
		}
		if err := copyFile(sourcePath, destinationPath, info.Mode().Perm()); err != nil {
			return fmt.Errorf("copy file %q: %w", sourcePath, err)
		}
	}
	return nil
}

func copyFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	return os.Chmod(destination, mode)
}

func npmPackageManifestPath(project Project) string {
	if project.NpmPackage != nil && project.NpmPackage.PackagePath != "" {
		return project.NpmPackage.PackagePath
	}
	return filepath.Join(filepath.Dir(GetConfigPath()), project.Name, "package.json")
}

func nextStandaloneDir(project Project) string {
	if project.NpmPackage == nil || project.NpmPackage.ProjectType != NextJS {
		return ""
	}
	deployedDir := nextStandaloneDeploymentDir(project)
	if fileExists(filepath.Join(deployedDir, "server.js")) {
		return deployedDir
	}
	return ""
}

func nuxtStandaloneDir(project Project) string {
	if project.NpmPackage == nil || project.NpmPackage.ProjectType != NuxtJS {
		return ""
	}
	deployedDir := nextStandaloneDeploymentDir(project)
	if fileExists(filepath.Join(deployedDir, "server", "index.mjs")) {
		return deployedDir
	}
	return ""
}

func standaloneProjectDir(project Project) string {
	if project.NpmPackage == nil {
		return ""
	}
	switch project.NpmPackage.ProjectType {
	case NextJS:
		return nextStandaloneDir(project)
	case NuxtJS:
		return nuxtStandaloneDir(project)
	default:
		return ""
	}
}

func nextStandaloneDeploymentDir(project Project) string {
	return filepath.Join(filepath.Dir(GetConfigPath()), "dist", project.Name)
}

func packageManagerForProject(projectPath string) string {
	for _, name := range []string{"bun.lock", "bun.lockb"} {
		if fileExists(filepath.Join(projectPath, name)) {
			return "bun"
		}
	}
	return "npm"
}

func projectBuildEnvironment(project Project) []string {
	env := os.Environ()
	for key, value := range project.EnvironmentVariables {
		env = append(env, key+"="+value)
	}
	if project.NpmPackage != nil && project.NpmPackage.ProjectType == NextJS {
		env = append(env,
			"NODE_OPTIONS=--max_old_space_size=8192",
			"RAYON_NUM_THREADS=4",
		)
	}
	return env
}

func runPackageManagerCommand(manager, workingDir string, output io.Writer, env []string, args ...string) error {
	if manager == "npm" {
		return runNpmCommandWithEnv(workingDir, output, env, args...)
	}
	if manager != "bun" {
		return fmt.Errorf("unsupported package manager %q", manager)
	}
	cmd := exec.Command("bun", args...)
	hideConsoleWindow(cmd)
	cmd.Dir = workingDir
	cmd.Env = env
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd.Run()
}

func runNpmCommand(workingDir string, output io.Writer, args ...string) error {
	return runNpmCommandWithEnv(workingDir, output, nil, args...)
}

func runNpmCommandWithEnv(workingDir string, output io.Writer, env []string, args ...string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		nodePath, err := nodeExecutablePath()
		if err != nil {
			return err
		}
		npmPath, err := npmCLIScriptPath()
		if err != nil {
			return err
		}
		cmd = exec.Command(nodePath, append([]string{npmPath}, args...)...)
	} else {
		cmd = exec.Command("npm", args...)
	}
	hideConsoleWindow(cmd)
	cmd.Dir = workingDir
	if env != nil {
		cmd.Env = env
	}

	// Using the same writer for both streams makes os/exec copy them through a
	// single goroutine, so output is serialized and interleaved in order, with
	// no manual pipes, WaitGroup or data races.
	cmd.Stdout = output
	cmd.Stderr = output

	return cmd.Run()
}

func runBuildCommand(workingDir string, output io.Writer, command string) error {
	return runBuildCommandWithEnv(workingDir, output, nil, command)
}

func runBuildCommandWithEnv(workingDir string, output io.Writer, env []string, command string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/d", "/c", normalizeWindowsBuildCommand(command))
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	hideConsoleWindow(cmd)
	cmd.Dir = workingDir
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd.Run()
}

func normalizeWindowsBuildCommand(command string) string {
	var normalized strings.Builder
	var quote byte
	for i := 0; i < len(command); i++ {
		char := command[i]
		if quote != 0 {
			normalized.WriteByte(char)
			if char == '\\' && i+1 < len(command) && command[i+1] == quote {
				i++
				normalized.WriteByte(command[i])
				continue
			}
			if char == quote {
				quote = 0
			}
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
			normalized.WriteByte(char)
		case ';':
			normalized.WriteString("&&")
		default:
			normalized.WriteByte(char)
		}
	}
	return normalized.String()
}
