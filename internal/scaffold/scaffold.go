package scaffold

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/devtime-ltd/slate/internal/config"
)

type Scaffold interface {
	Name() string
	FS() embed.FS
	FileMap(slateDir string) map[string]string
	RenderDockerfile(content string, cfg config.ProjectConfig) (string, error)
	DefaultFiles() map[string]string                                             // host path -> container path
	DefaultEnv(hostname string, globalCfg config.GlobalConfig) map[string]string // default .env.container vars
	Tools() map[string]config.Tool                                               // exec + db tools
	// Subdomains maps subdomain prefix -> compose service name + container port.
	// Empty key "" is the main app at hostname.test; "vite" becomes vite.hostname.test, etc.
	Subdomains() map[string]Subdomain
	// AppLikeServices lists services that share the /app bind. First is the
	// primary; /app/* file mounts go on it alone (others would race).
	AppLikeServices() []string
}

type Subdomain struct {
	Service string
	Port    int
}

var registry = map[string]Scaffold{}

func Register(s Scaffold) {
	registry[s.Name()] = s
}

func Get(name string) (Scaffold, error) {
	s, ok := registry[name]
	if !ok {
		names := make([]string, 0, len(registry))
		for n := range registry {
			names = append(names, n)
		}
		return nil, fmt.Errorf("unknown scaffold: %s (available: %s or none)", name, strings.Join(names, ", "))
	}
	return s, nil
}

func Generate(workspaceDir string, cfg config.ProjectConfig) error {
	slateDir := filepath.Join(workspaceDir, ".slate")
	if err := os.MkdirAll(slateDir, 0o755); err != nil {
		return fmt.Errorf("creating .slate dir: %w", err)
	}

	name := cfg.Scaffold
	if name == "" || name == "none" {
		return nil
	}

	s, err := Get(name)
	if err != nil {
		return err
	}

	fileMap := s.FileMap(slateDir)

	entries, err := s.FS().ReadDir(name)
	if err != nil {
		return fmt.Errorf("reading scaffold %s: %w", name, err)
	}

	for _, entry := range entries {
		destPath, ok := fileMap[entry.Name()]
		if !ok || destPath == "" {
			continue
		}

		data, err := s.FS().ReadFile(name + "/" + entry.Name())
		if err != nil {
			return fmt.Errorf("reading %s: %w", entry.Name(), err)
		}

		content := string(data)

		if entry.Name() == "Dockerfile.tmpl" {
			content, err = s.RenderDockerfile(content, cfg)
			if err != nil {
				return fmt.Errorf("rendering Dockerfile: %w", err)
			}
		}

		if err := os.WriteFile(destPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", destPath, err)
		}
	}

	return nil
}

func GenerateFileMounts(workspaceDir string, cfg config.ProjectConfig, s Scaffold) error {
	files := make(map[string]string)
	for k, v := range s.DefaultFiles() {
		files[k] = v
	}
	for k, v := range cfg.StringMap("files") {
		files[k] = v
	}

	slateDir := filepath.Join(workspaceDir, ".slate")
	composeOverride := filepath.Join(slateDir, "compose.files.yaml")
	filesDir := filepath.Join(slateDir, "files")

	for _, p := range []string{slateDir, composeOverride, filesDir} {
		if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to operate", p)
		}
	}

	if err := os.Remove(composeOverride); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "  warning: could not remove stale %s: %v\n", composeOverride, err)
	}

	if len(files) == 0 {
		return nil
	}

	// RemoveAll handles symlinks as symlinks (not following them), so a hostile
	// worktree planting .slate/files/file_N as a symlink can't redirect the
	// WriteFile below.
	if err := os.RemoveAll(filesDir); err != nil {
		return fmt.Errorf("clearing files dir: %w", err)
	}
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return fmt.Errorf("creating files dir: %w", err)
	}

	services := s.AppLikeServices()
	if len(services) == 0 {
		return fmt.Errorf("scaffold %q declares no AppLikeServices(); file mounts cannot be applied", s.Name())
	}
	primary := services[0]

	var sharedMounts, appOnlyMounts []string
	i := 0
	for source, target := range files {
		expanded := expandHome(source)
		data, err := os.ReadFile(expanded)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: skipping file mount %s -> %s (%v)\n", source, target, err)
			continue
		}

		localName := fmt.Sprintf("file_%d", i)
		localPath := filepath.Join(filesDir, localName)
		if err := os.WriteFile(localPath, data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", localPath, err)
		}

		mount := fmt.Sprintf("      - ./files/%s:%s:ro", localName, target)
		if strings.HasPrefix(target, "/app/") {
			appOnlyMounts = append(appOnlyMounts, mount)
		} else {
			sharedMounts = append(sharedMounts, mount)
		}
		i++
	}

	if len(sharedMounts) == 0 && len(appOnlyMounts) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("services:\n")
	for _, svc := range services {
		if svc != primary && len(sharedMounts) == 0 {
			continue
		}
		b.WriteString(fmt.Sprintf("  %s:\n    volumes:\n", svc))
		for _, m := range sharedMounts {
			b.WriteString(m + "\n")
		}
		if svc == primary {
			for _, m := range appOnlyMounts {
				b.WriteString(m + "\n")
			}
		}
	}

	return os.WriteFile(composeOverride, []byte(b.String()), 0o644)
}

var dbNameRe = regexp.MustCompile(`\{\{DB_NAME(?::([^}]*))?\}\}`)
var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func expandDBName(value, project, workspace string) string {
	return dbNameRe.ReplaceAllStringFunc(value, func(match string) string {
		sub := dbNameRe.FindStringSubmatch(match)
		label := "default"
		if len(sub) > 1 && sub[1] != "" {
			label = strings.TrimSpace(sub[1])
		}
		return DeriveDBName(project, workspace, label)
	})
}


func DeriveDBName(project, workspace, label string) string {
	ws := sanitiseDBSegment(workspace, 18)
	hash := shortHash(project, workspace, label)

	if label == "default" {
		return ensureLeadingAlpha(ws + "_" + hash)
	}

	lb := sanitiseDBSegment(label, 18)
	return ensureLeadingAlpha(ws + "_" + lb + "_" + hash)
}

func sanitiseDBSegment(s string, maxLen int) string {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if len(s) > maxLen {
		s = s[:maxLen]
		s = strings.TrimRight(s, "_")
	}
	return s
}

func ensureLeadingAlpha(s string) string {
	if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		return "x_" + s
	}
	return s
}

func shortHash(parts ...string) string {
	h := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(parts, ":"))))
	return h[:6]
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func ComposeFilePath(workspaceDir string) string {
	return filepath.Join(workspaceDir, ".slate", "compose.yaml")
}

func EnsureGitignore(projectDir string) error {
	gi := filepath.Join(projectDir, ".gitignore")
	data, _ := os.ReadFile(gi)
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == ".slate" || trimmed == ".slate/" {
			return nil
		}
	}

	f, err := os.OpenFile(gi, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if len(data) > 0 && data[len(data)-1] != '\n' {
		f.WriteString("\n")
	}
	_, err = f.WriteString(".slate/\n")
	return err
}

func GenerateEnvContainer(workspaceDir, hostname, project, workspace string, cfg config.ProjectConfig, globalCfg config.GlobalConfig) error {
	defaults := make(map[string]string)

	// Get scaffold-specific defaults if available
	if s, err := Get(cfg.Scaffold); err == nil {
		for k, v := range s.DefaultEnv(hostname, globalCfg) {
			defaults[k] = v
		}
	}

	// User overrides from slate.yml
	for k, v := range cfg.Env {
		defaults[k] = v
	}

	// Expand placeholders
	for k, v := range defaults {
		v = expandDBName(v, project, workspace)
		v = config.ExpandPasswords(v, globalCfg.SecretKey, project, workspace)
		defaults[k] = v
	}

	keys := make([]string, 0, len(defaults))
	for k := range defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, defaults[k])
	}

	return os.WriteFile(filepath.Join(workspaceDir, ".env.container"), []byte(b.String()), 0o644)
}

// BuildLifecycleScript assembles the full script for a lifecycle phase.
// slate new: up (install deps + migrate) then new (fresh DB)
// slate up:  up (install deps + migrate)
func BuildLifecycleScript(cfg config.ProjectConfig, isNew bool) string {
	var parts []string

	upScript := cfg.Up
	defaultUp := config.DefaultSetupForScaffold(cfg.Scaffold)
	if upScript == "" {
		upScript = defaultUp
	} else {
		upScript = strings.ReplaceAll(upScript, "{{SCAFFOLD_DEFAULT}}", defaultUp)
	}
	if upScript != "" {
		parts = append(parts, upScript)
	}

	if isNew {
		newScript := cfg.New
		defaultNew := config.DefaultFreshSetupForScaffold(cfg.Scaffold)
		if newScript == "" {
			newScript = defaultNew
		} else {
			newScript = strings.ReplaceAll(newScript, "{{SCAFFOLD_DEFAULT}}", defaultNew)
		}
		if newScript != "" {
			parts = append(parts, newScript)
		}
	}

	if len(parts) == 0 {
		return ""
	}

	script := "set -e\n"
	for _, p := range parts {
		script += strings.TrimRight(p, "\n") + "\n"
	}
	return script
}
