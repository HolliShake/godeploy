package function

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Project struct {
	Name                string
	PersonalAccessToken string
	RepositoryURL       string
	Branch              string
	GitUsername         string
	GitEmail            string
	NpmPackage          *NpmPackage
	PM2Process          *PM2Process
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
	output, err := exec.Command("git", "config", "--get", key).Output()
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
	if _, err := os.Stat(repoPath); err == nil {
		_, _ = fmt.Fprintf(output, "$ repository already exists at %s\n", repoPath)
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check repository path %q: %w", repoPath, err)
	}

	cloneURL, err := repositoryCloneURL(project)
	if err != nil {
		return err
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
		if out, err := reset.CombinedOutput(); err != nil {
			return fmt.Errorf("reset remote url: %w: %s", err, safeRedact(string(out), project.PersonalAccessToken))
		}
	}

	_, _ = fmt.Fprintf(output, "$ repository cloned successfully\n")
	return nil
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
