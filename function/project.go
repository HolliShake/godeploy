package function

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Project struct {
	Name                 string
	PersonalAccessToken  string
	RepositoryURL        string
	Branch               string
	GitUsername          string
	GitEmail             string
	BuildCommand         string
	NpmPackage           *NpmPackage
	PM2Process           *PM2Process
	EnvironmentVariables map[string]string
}

func ParseEnvironmentFile(reader io.Reader) (map[string]string, error) {
	variables := make(map[string]string)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("parse environment file line %d: expected KEY=VALUE", lineNumber)
		}
		key = strings.TrimSpace(key)
		if !validEnvironmentVariableName(key) {
			return nil, fmt.Errorf("parse environment file line %d: invalid variable name %q", lineNumber, key)
		}
		value, err := parseEnvironmentValue(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("parse environment file line %d: %w", lineNumber, err)
		}
		variables[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read environment file: %w", err)
	}
	return variables, nil
}

func WriteProjectEnvironmentFile(project Project) (string, error) {
	if project.NpmPackage == nil || project.NpmPackage.PackagePath == "" {
		if project.EnvironmentVariables == nil {
			return "", nil
		}
		return "", fmt.Errorf("cannot write project environment file without an npm package path")
	}

	projectPath := filepath.Dir(project.NpmPackage.PackagePath)
	variables := make(map[string]string)
	buildEnvironment := ""
	if project.EnvironmentVariables != nil {
		buildEnvironment = strings.TrimSpace(project.EnvironmentVariables["ENV"])
	}
	if buildEnvironment == "" {
		buildEnvironment = strings.TrimSpace(os.Getenv("ENV"))
	}
	if buildEnvironment != "" {
		if strings.ContainsAny(buildEnvironment, `/\`) {
			return "", fmt.Errorf("invalid ENV profile name %q", buildEnvironment)
		}
		profilePath := filepath.Join(projectPath, ".env."+buildEnvironment)
		file, err := os.Open(profilePath)
		if err == nil {
			profileVariables, parseErr := ParseEnvironmentFile(file)
			closeErr := file.Close()
			if parseErr != nil {
				return "", fmt.Errorf("parse environment profile %q: %w", profilePath, parseErr)
			}
			if closeErr != nil {
				return "", fmt.Errorf("close environment profile %q: %w", profilePath, closeErr)
			}
			for key, value := range profileVariables {
				variables[key] = value
			}
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("open environment profile %q: %w", profilePath, err)
		}
	}
	for key, value := range project.EnvironmentVariables {
		variables[key] = value
	}
	if project.EnvironmentVariables == nil && len(variables) == 0 {
		return "", nil
	}

	path := filepath.Join(projectPath, ".env")
	if err := WriteEnvironmentFile(path, variables); err != nil {
		return "", fmt.Errorf("write project environment file %q: %w", path, err)
	}
	return path, nil
}

func WriteEnvironmentFile(path string, variables map[string]string) error {
	keys := make([]string, 0, len(variables))
	for key := range variables {
		if !validEnvironmentVariableName(key) {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var contents strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&contents, "%s=%s\n", key, strconv.Quote(variables[key]))
	}
	if err := os.WriteFile(path, []byte(contents.String()), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set environment file permissions: %w", err)
	}
	return nil
}

func validEnvironmentVariableName(name string) bool {
	if name == "" || !(name[0] == '_' || name[0] >= 'A' && name[0] <= 'Z' || name[0] >= 'a' && name[0] <= 'z') {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if c != '_' && !(c >= 'A' && c <= 'Z') && !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func parseEnvironmentValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] != '\'' && value[0] != '"' {
		for i := 0; i < len(value); i++ {
			if value[i] == '#' && (i == 0 || value[i-1] == ' ' || value[i-1] == '\t') {
				value = value[:i]
				break
			}
		}
		return strings.TrimSpace(value), nil
	}

	quote := value[0]
	escaped := false
	end := -1
	for i := 1; i < len(value); i++ {
		if quote == '"' && value[i] == '\\' && !escaped {
			escaped = true
			continue
		}
		if value[i] == quote && !escaped {
			end = i
			break
		}
		escaped = false
	}
	if end < 0 {
		return "", fmt.Errorf("unterminated quoted value")
	}
	if trailing := strings.TrimSpace(value[end+1:]); trailing != "" && !strings.HasPrefix(trailing, "#") {
		return "", fmt.Errorf("unexpected text after quoted value")
	}
	if quote == '\'' {
		return value[1:end], nil
	}
	unquoted, err := strconv.Unquote(value[:end+1])
	if err != nil {
		return "", fmt.Errorf("invalid double-quoted value: %w", err)
	}
	return unquoted, nil
}

func CreateNewProject(state *State, project Project) error {
	if err := validateProjectName(project.Name); err != nil {
		return err
	}
	projects := append([]Project(nil), state.Config.Projects...)
	for _, existing := range projects {
		if strings.EqualFold(strings.TrimSpace(existing.Name), strings.TrimSpace(project.Name)) {
			return fmt.Errorf("project %q already exists", project.Name)
		}
	}

	if strings.TrimSpace(project.GitUsername) == "" {
		gitUsername, err := readGitConfig("user.name")
		if err != nil {
			return fmt.Errorf("read git user.name: %w", err)
		}
		project.GitUsername = gitUsername
	}
	if strings.TrimSpace(project.GitEmail) == "" {
		gitEmail, err := readGitConfig("user.email")
		if err != nil {
			return fmt.Errorf("read git user.email: %w", err)
		}
		project.GitEmail = gitEmail
	}

	project.NpmPackage, _ = DetectNpmPackage(project)

	projects = append(projects, project)
	if err := writeProjects(state, projects); err != nil {
		return err
	}
	return nil
}

func readGitConfig(key string) (string, error) {
	cmd := exec.Command("git", "config", "--get", key)
	hideConsoleWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func UpdateProject(state *State, originalName string, project Project) error {
	if err := validateProjectName(project.Name); err != nil {
		return err
	}
	projects := append([]Project(nil), state.Config.Projects...)
	index := -1
	for i, existing := range projects {
		if strings.EqualFold(strings.TrimSpace(existing.Name), strings.TrimSpace(originalName)) {
			index = i
			break
		}
	}
	if index == -1 {
		return fmt.Errorf("project %q not found", originalName)
	}
	for i, existing := range projects {
		if i != index && strings.EqualFold(strings.TrimSpace(existing.Name), strings.TrimSpace(project.Name)) {
			return fmt.Errorf("project %q already exists", project.Name)
		}
	}
	if strings.TrimSpace(project.GitUsername) == "" {
		gitUsername, err := readGitConfig("user.name")
		if err != nil {
			return fmt.Errorf("read git user.name: %w", err)
		}
		project.GitUsername = gitUsername
	}
	if strings.TrimSpace(project.GitEmail) == "" {
		gitEmail, err := readGitConfig("user.email")
		if err != nil {
			return fmt.Errorf("read git user.email: %w", err)
		}
		project.GitEmail = gitEmail
	}
	projects[index] = project
	return writeProjects(state, projects)
}

func DeleteProject(state *State, name string) error {
	projects := append([]Project(nil), state.Config.Projects...)
	for i, project := range projects {
		if strings.EqualFold(strings.TrimSpace(project.Name), strings.TrimSpace(name)) {
			projects = append(projects[:i], projects[i+1:]...)
			return writeProjects(state, projects)
		}
	}
	return fmt.Errorf("project %q not found", name)
}

func writeProjects(state *State, projects []Project) error {
	config := state.Config
	config.Projects = projects
	if err := WriteConfig(state.ConfigPath, config); err != nil {
		return err
	}
	state.Config.Projects = projects
	return nil
}

func validateProjectName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("project name is required")
	}
	if trimmed != name {
		return fmt.Errorf("project name cannot start or end with whitespace")
	}
	if trimmed == "." || trimmed == ".." {
		return fmt.Errorf("project name %q cannot be used as a folder name", name)
	}
	if len([]rune(name)) > 255 {
		return fmt.Errorf("project name %q is too long for a folder name", name)
	}
	if strings.ContainsAny(name, `<>:"/\|?*`) {
		return fmt.Errorf("project name %q contains characters that are invalid in folder names", name)
	}
	for _, char := range name {
		if char < 32 {
			return fmt.Errorf("project name %q contains a control character", name)
		}
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return fmt.Errorf("project name %q cannot end with a dot or space", name)
	}

	reserved := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch reserved {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return fmt.Errorf("project name %q is reserved by the operating system", name)
	}
	return nil
}

func FetchRepository(project Project) error {
	return FetchRepositoryWithOutput(project, io.Discard)
}

func FetchRepositoryWithOutput(project Project, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	base := filepath.Dir(GetConfigPath())
	repoPath := filepath.Join(base, project.Name)
	cloneURL, err := repositoryCloneURL(project)
	if err != nil {
		return err
	}

	if info, err := os.Stat(repoPath); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("repository path %q is not a directory", repoPath)
		}
		return updateExistingRepository(project, repoPath, cloneURL, output)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check repository path %q: %w", repoPath, err)
	}

	// Redact the token from anything git prints (it echoes URLs in errors).
	safeOutput := &redactWriter{w: output, secret: strings.TrimSpace(project.PersonalAccessToken)}

	branch := strings.TrimSpace(project.Branch)
	if branch != "" {
		_, _ = fmt.Fprintf(output, "$ git clone --progress -b %s %s %s\n", branch, project.RepositoryURL, repoPath)
	} else {
		_, _ = fmt.Fprintf(output, "$ git clone --progress %s %s\n", project.RepositoryURL, repoPath)
	}

	// --progress forces git to print progress even though stderr is not a TTY
	args := []string{"clone", "--progress"}
	if branch != "" {
		args = append(args, "-b", branch)
	}
	args = append(args, cloneURL, repoPath)

	cmd := exec.Command("git", args...)
	hideConsoleWindow(cmd)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0") // fail instead of hanging on a prompt
	// Same writer for both streams so exec serializes the copying.
	cmd.Stdout = safeOutput
	cmd.Stderr = safeOutput
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(repoPath) // clean up partial clone
		return fmt.Errorf("failed to clone repository: %w", err)
	}

	// Strip the token from .git/config so it isn't persisted on disk.
	if project.PersonalAccessToken != "" {
		cleanURL := "https://" + normalizeRepositoryPath(project.RepositoryURL)
		reset := exec.Command("git", "-C", repoPath, "remote", "set-url", "origin", cleanURL)
		hideConsoleWindow(reset)
		if out, err := reset.CombinedOutput(); err != nil {
			return fmt.Errorf("reset remote url: %w: %s", err, safeRedact(string(out), project.PersonalAccessToken))
		}
	}

	_, _ = fmt.Fprintf(output, "$ repository cloned successfully\n")
	return nil
}

func updateExistingRepository(project Project, repoPath, cloneURL string, output io.Writer) error {
	safeOutput := &redactWriter{w: output, secret: strings.TrimSpace(project.PersonalAccessToken)}
	checkout := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	hideConsoleWindow(checkout)
	checkout.Dir = repoPath
	result, err := checkout.Output()
	if err != nil || strings.TrimSpace(string(result)) != "true" {
		if err != nil {
			return fmt.Errorf("existing project directory %q is not a Git checkout: %w", repoPath, err)
		}
		return fmt.Errorf("existing project directory %q is not a Git checkout", repoPath)
	}

	displayURL := safeRedact(project.RepositoryURL, strings.TrimSpace(project.PersonalAccessToken))
	branch := strings.TrimSpace(project.Branch)
	if branch != "" {
		_, _ = fmt.Fprintf(output, "$ git fetch --progress %s %s\n", displayURL, branch)
		if err := runGitCommand(repoPath, safeOutput, "fetch", "--progress", cloneURL, branch); err != nil {
			return fmt.Errorf("fetch branch %q: %w", branch, err)
		}
		exists, err := localBranchExists(repoPath, branch)
		if err != nil {
			return err
		}
		if exists {
			_, _ = fmt.Fprintf(output, "$ git checkout %s\n", branch)
			if err := runGitCommand(repoPath, safeOutput, "checkout", branch); err != nil {
				return fmt.Errorf("checkout branch %q: %w", branch, err)
			}
		} else {
			_, _ = fmt.Fprintf(output, "$ git checkout -b %s FETCH_HEAD\n", branch)
			if err := runGitCommand(repoPath, safeOutput, "checkout", "-b", branch, "FETCH_HEAD"); err != nil {
				return fmt.Errorf("create branch %q from fetched repository: %w", branch, err)
			}
		}
		_, _ = fmt.Fprintf(output, "$ git pull --progress %s %s\n", displayURL, branch)
		if err := runGitCommand(repoPath, safeOutput, "pull", "--progress", cloneURL, branch); err != nil {
			return fmt.Errorf("pull branch %q: %w", branch, err)
		}
	} else {
		_, _ = fmt.Fprintf(output, "$ git pull --progress %s\n", displayURL)
		if err := runGitCommand(repoPath, safeOutput, "pull", "--progress", cloneURL); err != nil {
			return fmt.Errorf("pull repository updates: %w", err)
		}
	}

	_, _ = fmt.Fprintln(output, "Repository updated successfully.")
	return nil
}

func runGitCommand(repoPath string, output io.Writer, args ...string) error {
	cmd := exec.Command("git", args...)
	hideConsoleWindow(cmd)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd.Run()
}

func localBranchExists(repoPath, branch string) (bool, error) {
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	hideConsoleWindow(cmd)
	cmd.Dir = repoPath
	if err := cmd.Run(); err == nil {
		return true, nil
	} else {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("check local branch %q: %w", branch, err)
	}
}

func repositoryCloneURL(project Project) (string, error) {
	token := strings.TrimSpace(project.PersonalAccessToken)
	if token == "" {
		return project.RepositoryURL, nil
	}

	repoPath := normalizeRepositoryPath(project.RepositoryURL)
	if repoPath == "" {
		return "", fmt.Errorf("repository URL is required")
	}
	parsed, err := url.Parse("https://" + repoPath)
	if err != nil {
		return "", fmt.Errorf("parse repository URL: %w", err)
	}
	// GitHub accepts the token as the username; GitLab/Bitbucket expect a
	// username + token pair (GitLab: "oauth2", Bitbucket: "x-token-auth").
	parsed.User = url.UserPassword("x-access-token", token)
	return parsed.String(), nil
}

type redactWriter struct {
	w      io.Writer
	secret string
}

func (r *redactWriter) Write(p []byte) (int, error) {
	if r.secret == "" {
		return r.w.Write(p)
	}
	redacted := strings.ReplaceAll(string(p), r.secret, "***")
	if _, err := r.w.Write([]byte(redacted)); err != nil {
		return 0, err
	}
	return len(p), nil // report original length so exec doesn't see a short write
}

func safeRedact(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "***")
}

func normalizeRepositoryPath(repositoryURL string) string {
	repoPath := strings.TrimSpace(repositoryURL)
	repoPath = strings.TrimPrefix(repoPath, "https://")
	repoPath = strings.TrimPrefix(repoPath, "http://")
	repoPath = strings.TrimSuffix(repoPath, "/")
	return strings.TrimSpace(strings.SplitN(repoPath, "?", 2)[0])
}
