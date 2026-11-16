package function

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

type Config struct {
	Projects []Project `json:"projects"`
}

const AppName = "GoDeploy"

func GetConfigPath() string {
	switch runtime.GOOS {
	case "windows":
		if base := os.Getenv("APPDATA"); base != "" {
			return filepath.Join(base, AppName, "config.json")
		}
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, AppName, "config.json")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, AppName, "config.json")
		}

	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", AppName, "config.json")
		}

	default: // linux, bsd, etc.
		if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
			return filepath.Join(base, AppName, "config.json")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".config", AppName, "config.json")
		}
	}

	return "config.json"
}

func ReadConfig() (Config, error) {
	path := GetConfigPath()

	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return createDefaultConfig(path)
		}
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	var config Config
	if err := json.NewDecoder(file).Decode(&config); err != nil {
		return Config{Projects: []Project{}}, fmt.Errorf("decode config %q: %w", path, err)
	}

	if config.Projects == nil {
		config.Projects = []Project{}
	}

	for i := range config.Projects {
		config.Projects[i].NpmPackage, _ = DetectNpmPackage(config.Projects[i])
	}

	return config, nil
}

func WriteConfig(path string, config Config) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create config %q: %w", path, err)
	}
	defer file.Close()

	if err := json.NewEncoder(file).Encode(config); err != nil {
		return fmt.Errorf("encode config %q: %w", path, err)
	}

	_ = os.Chmod(path, 0o600)

	return nil
}

func createDefaultConfig(path string) (Config, error) {
	cfg := Config{Projects: []Project{}}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Config{}, fmt.Errorf("create config dir %q: %w", filepath.Dir(path), err)
	}

	file, err := os.Create(path)
	if err != nil {
		return Config{}, fmt.Errorf("create config %q: %w", path, err)
	}
	defer file.Close()

	if err := json.NewEncoder(file).Encode(cfg); err != nil {
		return Config{}, fmt.Errorf("encode config %q: %w", path, err)
	}

	_ = os.Chmod(path, 0o600)

	return cfg, nil
}
