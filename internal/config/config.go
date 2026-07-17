package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type GlobalConfig struct {
	HTTPPort  int    `yaml:"http_port"`
	HTTPSPort int    `yaml:"https_port"`
	TLS       bool   `yaml:"tls"`
	SecretKey string `yaml:"secret_key"`
	Editor    string `yaml:"editor"`
	// AutoCd controls whether `slate new` and `slate up` drop into a shell
	// at the workspace dir after provisioning. Override per-invocation with
	// --cd or --cd=false.
	AutoCd bool `yaml:"auto_cd"`
}

// Tool is implemented by ExecTool and DBTool. The marker method keeps the
// interface closed to slate-defined tool kinds.
type Tool interface {
	isTool()
}

type ExecTool struct {
	Service string   `yaml:"service"`
	Command []string `yaml:"command"`
}

func (ExecTool) isTool() {}

type DBTool struct {
	Service      string `yaml:"service"`
	Port         int    `yaml:"port"`
	Scheme       string `yaml:"scheme"`
	User         string `yaml:"user"`
	PasswordSalt string `yaml:"password_salt"`
}

func (DBTool) isTool() {}

type ProjectConfig struct {
	Scaffold string              `yaml:"scaffold"`
	Project  string              `yaml:"project"`
	Database string              `yaml:"database"`
	Editor   string              `yaml:"editor"`
	Fresh    string              `yaml:"fresh"`
	Setup    string              `yaml:"setup"`
	Env      map[string]string   `yaml:"env"`
	AppPort  int                 `yaml:"app_port"`
	VitePort int                 `yaml:"vite_port"`
	Tools    map[string]ExecTool `yaml:"tools"`
	Agent    AgentCmd            `yaml:"agent"`
	Up       string              `yaml:"up"`
	Extra    map[string]any      `yaml:",inline"`
}

// AgentCmd is a single command, or a [first-run, thereafter] pair.
type AgentCmd struct {
	First string
	Again string
}

func (a *AgentCmd) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		a.First, a.Again = s, s
		return nil
	case yaml.SequenceNode:
		var items []string
		if err := value.Decode(&items); err != nil {
			return fmt.Errorf("agent: %w", err)
		}
		switch len(items) {
		case 1:
			a.First, a.Again = items[0], items[0]
		case 2:
			a.First, a.Again = items[0], items[1]
		default:
			return fmt.Errorf("agent: expected 1 or 2 commands, got %d", len(items))
		}
		return nil
	}
	return fmt.Errorf("agent: expected a command string or a [first-run, thereafter] list")
}

func (a AgentCmd) IsZero() bool {
	return a.First == "" && a.Again == ""
}

func (c ProjectConfig) StringMap(key string) map[string]string {
	val, ok := c.Extra[key]
	if !ok {
		return nil
	}
	v, ok := val.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(v))
	for k, item := range v {
		// Accept any scalar by stringifying; lets users write
		// `max_execution_time: 0` without quoting.
		switch s := item.(type) {
		case string:
			out[k] = s
		case int, int64, float64, bool:
			out[k] = fmt.Sprint(s)
		}
	}
	return out
}

func (c ProjectConfig) StringSlice(key string) []string {
	val, ok := c.Extra[key]
	if !ok {
		return nil
	}
	switch v := val.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func DefaultGlobal() GlobalConfig {
	return GlobalConfig{
		HTTPPort:  80,
		HTTPSPort: 443,
		TLS:       true,
		AutoCd:    true,
	}
}

func DefaultProject() ProjectConfig {
	return ProjectConfig{
		AppPort:  8080,
		VitePort: 5173,
	}
}

func DefaultSetupForScaffold(scaffold string) string {
	switch scaffold {
	case "laravel":
		// migrate (no --seed): the up hook runs on every `slate new` AND every
		// `slate up`, so seeding here re-runs all seeders on each restart and
		// duplicates data. Initial seeding is handled by the `new`/`--fresh`
		// hook (migrate:fresh --seed) below.
		return `retry composer install --no-progress --no-interaction
php artisan storage:link --force 2>/dev/null || true
grep -q '^APP_KEY=base64:' .env || php artisan key:generate --ansi
php artisan migrate --force
`
	case "nextjs":
		// deps install via the app container command; DB is opt-in
		return ""
	default:
		return ""
	}
}

func DefaultFreshSetupForScaffold(scaffold string) string {
	switch scaffold {
	case "laravel":
		return `php artisan migrate:fresh --seed --force
`
	case "nextjs":
		return ""
	default:
		return ""
	}
}

func (c *ProjectConfig) ResolvedTools() map[string]Tool {
	if len(c.Tools) == 0 {
		return nil
	}
	out := make(map[string]Tool, len(c.Tools))
	for k, v := range c.Tools {
		out[k] = v
	}
	return out
}

func GlobalConfigDir() string {
	if dir := os.Getenv("SLATE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "slate")
}

func DataDir() string {
	if dir := os.Getenv("SLATE_DATA_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "slate")
}

func LoadGlobal() (GlobalConfig, error) {
	cfg := DefaultGlobal()
	path := filepath.Join(GlobalConfigDir(), "config.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, nil
	}
	// Use a separate struct for unmarshalling so we can detect which fields
	// were explicitly set vs left at zero value.
	var raw struct {
		HTTPPort  *int    `yaml:"http_port"`
		HTTPSPort *int    `yaml:"https_port"`
		TLS       *bool   `yaml:"tls"`
		SecretKey *string `yaml:"secret_key"`
		Editor    *string `yaml:"editor"`
		AutoCd    *bool   `yaml:"auto_cd"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return cfg, err
	}
	if raw.HTTPPort != nil {
		cfg.HTTPPort = *raw.HTTPPort
	}
	if raw.HTTPSPort != nil {
		cfg.HTTPSPort = *raw.HTTPSPort
	}
	if raw.TLS != nil {
		cfg.TLS = *raw.TLS
	}
	if raw.SecretKey != nil {
		cfg.SecretKey = *raw.SecretKey
	}
	if raw.Editor != nil {
		cfg.Editor = *raw.Editor
	}
	if raw.AutoCd != nil {
		cfg.AutoCd = *raw.AutoCd
	}
	return cfg, nil
}

func LoadProject(dir string) (ProjectConfig, error) {
	cfg := DefaultProject()
	path := filepath.Join(dir, "slate.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, nil
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.AppPort == 0 {
		cfg.AppPort = 8080
	}
	if cfg.VitePort == 0 {
		cfg.VitePort = 5173
	}
	return cfg, nil
}

// LoadProjectForWorkspace prefers the worktree's slate.yml so a branch can
// test config changes. `project:` stays pinned to the main checkout, as do
// the host-executed `agent`/`up`: the worktree is container-writable, so
// honouring its copy would hand host execution to a rogue dependency.
func LoadProjectForWorkspace(mainRoot, wsDir string) (ProjectConfig, error) {
	mainCfg, err := LoadProject(mainRoot)
	if err != nil {
		return mainCfg, err
	}
	if wsDir == "" {
		return mainCfg, nil
	}
	if info, err := os.Stat(filepath.Join(wsDir, "slate.yml")); err != nil || info.IsDir() {
		return mainCfg, nil
	}
	wsCfg, err := LoadProject(wsDir)
	if err != nil {
		return wsCfg, fmt.Errorf("workspace slate.yml: %w", err)
	}
	wsCfg.Project = mainCfg.Project
	wsCfg.Agent = mainCfg.Agent
	wsCfg.Up = mainCfg.Up
	return wsCfg, nil
}

// HostExecPinned reports which pinned host-exec fields the workspace's
// slate.yml tries (inertly) to change.
func HostExecPinned(mainRoot, wsDir string) []string {
	if wsDir == "" {
		return nil
	}
	if info, err := os.Stat(filepath.Join(wsDir, "slate.yml")); err != nil || info.IsDir() {
		return nil
	}
	mainCfg, err := LoadProject(mainRoot)
	if err != nil {
		return nil
	}
	wsCfg, err := LoadProject(wsDir)
	if err != nil {
		return nil
	}
	var pinned []string
	if !wsCfg.Agent.IsZero() && wsCfg.Agent != mainCfg.Agent {
		pinned = append(pinned, "agent")
	}
	if wsCfg.Up != "" && wsCfg.Up != mainCfg.Up {
		pinned = append(pinned, "up")
	}
	return pinned
}
