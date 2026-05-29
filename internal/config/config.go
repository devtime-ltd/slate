package config

import (
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
	Editor   string              `yaml:"editor"`
	New      string              `yaml:"new"`
	Up       string              `yaml:"up"`
	Env      map[string]string   `yaml:"env"`
	AppPort  int                 `yaml:"app_port"`
	VitePort int                 `yaml:"vite_port"`
	Tools    map[string]ExecTool `yaml:"tools"`
	Extra    map[string]any      `yaml:",inline"`
}

func (c ProjectConfig) StringMap(key string) map[string]string {
	val, ok := c.Extra[key]
	if !ok {
		return nil
	}
	switch v := val.(type) {
	case map[string]any:
		out := make(map[string]string, len(v))
		for k, item := range v {
			if s, ok := item.(string); ok {
				out[k] = s
			}
		}
		return out
	default:
		return nil
	}
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
		return `composer install --no-progress --no-interaction
php artisan storage:link --force 2>/dev/null || true
grep -q '^APP_KEY=base64:' .env || php artisan key:generate --ansi
php artisan migrate --seed --force
`
	case "nextjs":
		return `npm install
npx prisma migrate deploy 2>/dev/null || true
`
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
		return `npx prisma migrate reset --force 2>/dev/null || true
npx prisma db seed 2>/dev/null || true
`
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
