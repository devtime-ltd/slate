package config

import (
	"fmt"
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

	for _, p := range ListProjects() {
		if p == abs {
			return nil
		}
	}

	name := uniqueNameFor(filepath.Base(abs))

	os.MkdirAll(GlobalConfigDir(), 0o755)
	f, err := os.OpenFile(RegistryPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%s=%s\n", name, abs)
	return err
}

// ListProjects returns the absolute paths of registered projects.
func ListProjects() []string {
	byName := ProjectsByName()
	out := make([]string, 0, len(byName))
	for _, path := range byName {
		out = append(out, path)
	}
	return out
}

// ProjectsByName returns a name -> path map, with names fixed at registration
// time so removals don't shift other names.
func ProjectsByName() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(RegistryPath())
	if err != nil {
		return out
	}

	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Old format (path only) -> use basename as name
		if !strings.Contains(line, "=") {
			out[filepath.Base(line)] = line
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		name := strings.TrimSpace(parts[0])
		path := strings.TrimSpace(parts[1])
		if name != "" && path != "" {
			out[name] = path
		}
	}
	return out
}

func uniqueNameFor(base string) string {
	existing := ProjectsByName()
	if _, taken := existing[base]; !taken {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, taken := existing[candidate]; !taken {
			return candidate
		}
	}
}
