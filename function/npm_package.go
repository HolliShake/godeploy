package function

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

type NpmPackageType string

const (
	Module   NpmPackageType = "module"
	CommonJS NpmPackageType = "commonjs" // Default
)

type NpmPackage struct {
	Name    string
	Version string
	Type    NpmPackageType
	Scripts map[string]string
}

func DetectNpmPackage(project Project) (*NpmPackage, error) {
	configPath := GetConfigPath()
	projectPath := filepath.Join(filepath.Dir(configPath), project.Name)
	packagePath := filepath.Join(projectPath, "package.json")

	data, err := os.ReadFile(packagePath)
	if err != nil {
		return nil, fmt.Errorf("read npm package manifest %q: %w", packagePath, err)
	}

	var manifest struct {
		Name    string            `json:"name"`
		Version string            `json:"version"`
		Type    string            `json:"type"`
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse npm package manifest %q: %w", packagePath, err)
	}

	packageType := CommonJS
	switch manifest.Type {
	case "":
	case string(Module):
		packageType = Module
	case string(CommonJS):
		packageType = CommonJS
	default:
		return nil, fmt.Errorf("unsupported npm package type %q in %q", manifest.Type, packagePath)
	}

	if manifest.Scripts == nil {
		manifest.Scripts = map[string]string{}
	}

	return &NpmPackage{
		Name:    manifest.Name,
		Version: manifest.Version,
		Type:    packageType,
		Scripts: manifest.Scripts,
	}, nil
}

func NpmInstallWithOutput(project Project, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}

	packagePath := filepath.Join(filepath.Dir(GetConfigPath()), project.Name, "package.json")
	if _, err := os.Stat(packagePath); os.IsNotExist(err) {
		return fmt.Errorf("npm package manifest not found at %q", packagePath)
	} else if err != nil {
		return fmt.Errorf("check npm package manifest %q: %w", packagePath, err)
	}

	args := []string{"install", "--foreground-scripts", "--no-progress", "--loglevel=info"}
	_, _ = fmt.Fprintln(output, "$ npm install --foreground-scripts --no-progress --loglevel=info")
	if err := runNpmCommand(filepath.Dir(packagePath), output, args...); err != nil {
		_, _ = fmt.Fprintf(output, "\nInitial npm install failed: %v\nRetrying with --legacy-peer-deps...\n", err)
		retryArgs := append(append([]string(nil), args...), "--legacy-peer-deps")
		_, _ = fmt.Fprintln(output, "$ npm install --foreground-scripts --no-progress --loglevel=info --legacy-peer-deps")
		if retryErr := runNpmCommand(filepath.Dir(packagePath), output, retryArgs...); retryErr != nil {
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
	buildScript, ok := project.NpmPackage.Scripts["build"]
	if !ok {
		_, _ = fmt.Fprintln(output, "No build script configured; skipping npm build.")
		return nil
	}

	projectPath := filepath.Join(filepath.Dir(GetConfigPath()), project.Name)
	args := []string{"--foreground-scripts", "--no-progress", "--loglevel=info", "run", "build"}
	_, _ = fmt.Fprintln(output, "$ npm --foreground-scripts --no-progress --loglevel=info run build")
	_, _ = fmt.Fprintf(output, "> %s\n", buildScript)
	if err := runNpmCommand(projectPath, output, args...); err != nil {
		return fmt.Errorf("run npm build in %q: %w", projectPath, err)
	}

	distPath := filepath.Join(projectPath, "dist")
	info, err := os.Stat(distPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("npm build completed but expected output directory %q was not created", distPath)
	}
	if err != nil {
		return fmt.Errorf("check npm build output directory %q: %w", distPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("npm build output path %q is not a directory", distPath)
	}

	_, _ = fmt.Fprintf(output, "npm build completed. Output directory: %s\n", distPath)
	return nil
}

func runNpmCommand(workingDir string, output io.Writer, args ...string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", append([]string{"/c", "npm"}, args...)...)
	} else {
		cmd = exec.Command("npm", args...)
	}
	cmd.Dir = workingDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create npm stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("create npm stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	var stdoutErr, stderrErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, stdoutErr = io.Copy(output, stdout)
	}()
	go func() {
		defer wg.Done()
		_, stderrErr = io.Copy(output, stderr)
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		return err
	}
	if stdoutErr != nil {
		return fmt.Errorf("stream npm stdout: %w", stdoutErr)
	}
	if stderrErr != nil {
		return fmt.Errorf("stream npm stderr: %w", stderrErr)
	}
	return nil
}
