package config

import (
	"os"
	"path/filepath"
	"strings"
)

func RegistryPath() string {
	return filepath.Join(GlobalConfigDir(), "projects")
}

func RegisterProject(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	existing := ListProjects()
	for _, p := range existing {
		if p == abs {
			return nil
		}
	}

	os.MkdirAll(GlobalConfigDir(), 0o755)
	f, err := os.OpenFile(RegistryPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(abs + "\n")
	return err
}

func ListProjects() []string {
	data, err := os.ReadFile(RegistryPath())
	if err != nil {
		return nil
	}

	var projects []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			projects = append(projects, line)
		}
	}
	return projects
}
