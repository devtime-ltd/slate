package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/devtime-ltd/slate/internal/workspace"
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
	Scaffold ScaffoldRef         `yaml:"scaffold"`
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
	New      string              `yaml:"new"`
	Up       string              `yaml:"up"`
	Doctor   map[string]string   `yaml:"doctor"`
	Brief    string              `yaml:"brief"`
	Extra    map[string]any      `yaml:",inline"`
}

// ScaffoldRef is a built-in scaffold name, or an inline scaffold when
// `scaffold:` is a map.
type ScaffoldRef struct {
	Name   string
	Inline *InlineScaffold
}

// InlineScaffold: a project-committed compose file (project-relative path;
// .tmpl extension opts into template rendering) plus the proxy routes it
// serves. Tools, lifecycle, env, and files stay top-level slate.yml keys.
type InlineScaffold struct {
	Compose    string                 `yaml:"compose"`
	Subdomains map[string]ServicePort `yaml:"subdomains"`
	Vars       map[string]any         `yaml:"vars"`
}

type ServicePort struct {
	Service string `yaml:"service"`
	Port    int    `yaml:"port"`
}

func (s *ScaffoldRef) UnmarshalYAML(value *yaml.Node) error {
	*s = ScaffoldRef{}
	switch value.Kind {
	case yaml.ScalarNode:
		return value.Decode(&s.Name)
	case yaml.MappingNode:
		var def InlineScaffold
		if err := value.Decode(&def); err != nil {
			return fmt.Errorf("scaffold: %w", err)
		}
		if def.Compose != "" && !filepath.IsLocal(filepath.Clean(def.Compose)) {
			return fmt.Errorf("scaffold: compose path %q must be relative and stay inside the project", def.Compose)
		}
		// "@" is the apex spelling (DNS-style); "" is the internal form the
		// proxy layer uses and isn't accepted in config.
		if _, ok := def.Subdomains[""]; ok {
			return fmt.Errorf(`scaffold: subdomains key "" is not allowed; use "@" for the main hostname`)
		}
		if apex, ok := def.Subdomains["@"]; ok {
			delete(def.Subdomains, "@")
			def.Subdomains[""] = apex
		}
		for prefix, sp := range def.Subdomains {
			label := prefix
			if label == "" {
				label = "@"
			}
			if sp.Service == "" {
				return fmt.Errorf("scaffold: subdomain %q needs a service", label)
			}
			if sp.Port < 1 || sp.Port > 65535 {
				return fmt.Errorf("scaffold: subdomain %q needs a port between 1 and 65535, got %d", label, sp.Port)
			}
		}
		s.Inline = &def
		return nil
	}
	return fmt.Errorf("scaffold: expected a scaffold name or an inline scaffold map")
}

func (s ScaffoldRef) IsZero() bool { return s.Name == "" && s.Inline == nil }

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

func (c ProjectConfig) String(key string) string {
	val, ok := c.Extra[key]
	if !ok {
		return ""
	}
	switch v := val.(type) {
	case string:
		return v
	case int, int64, float64, bool:
		return fmt.Sprint(v)
	default:
		return ""
	}
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

// LocalConfigName is the per-developer overlay next to the main checkout's
// slate.yml. Like the host-exec keys it may set, it is only ever read from
// the main checkout root: a copy inside a container-writable worktree is
// inert.
const LocalConfigName = "slate.local.yml"

// LocalOverridableKeys is the whole surface slate.local.yml may set: the
// host-executing keys. Container-reaching config (scaffold, env, files, ...)
// is deliberately excluded. New host-side keys join this one list.
var LocalOverridableKeys = []string{"agent", "brief", "doctor", "new", "up"}

// LoadMainProject loads the main checkout's slate.yml with the developer's
// slate.local.yml overlay applied. It is the loader for anything consuming
// host-exec config from the main checkout.
func LoadMainProject(mainRoot string) (ProjectConfig, error) {
	cfg, err := LoadProject(mainRoot)
	if err != nil {
		return cfg, err
	}
	return applyLocalOverlay(mainRoot, cfg)
}

// applyLocalOverlay replaces each host-side key present in slate.local.yml
// wholesale: a local `agent:` supersedes the committed value entirely, pair
// form included, and absent keys fall through to slate.yml. Any key outside
// LocalOverridableKeys is an error rather than being silently ignored.
func applyLocalOverlay(mainRoot string, cfg ProjectConfig) (ProjectConfig, error) {
	data, err := os.ReadFile(filepath.Join(mainRoot, LocalConfigName))
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		// any other failure would silently drop the developer's overrides
		return cfg, fmt.Errorf("%s: %w", LocalConfigName, err)
	}
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return cfg, fmt.Errorf("%s: %w", LocalConfigName, err)
	}
	var disallowed []string
	for key := range raw {
		if !slices.Contains(LocalOverridableKeys, key) {
			disallowed = append(disallowed, key)
		}
	}
	if len(disallowed) > 0 {
		sort.Strings(disallowed)
		return cfg, fmt.Errorf("%s: `%s` cannot be overridden locally; only the host-side keys `%s` can",
			LocalConfigName, strings.Join(disallowed, "` and `"), strings.Join(LocalOverridableKeys, "`, `"))
	}
	decode := func(key string, into any) error {
		node := raw[key]
		if err := node.Decode(into); err != nil {
			return fmt.Errorf("%s: %s: %w", LocalConfigName, key, err)
		}
		return nil
	}
	for key := range raw {
		var err error
		switch key {
		case "agent":
			cfg.Agent = AgentCmd{}
			err = decode(key, &cfg.Agent)
		case "brief":
			cfg.Brief = ""
			err = decode(key, &cfg.Brief)
		case "doctor":
			cfg.Doctor = nil
			err = decode(key, &cfg.Doctor)
		case "new":
			cfg.New = ""
			err = decode(key, &cfg.New)
		case "up":
			cfg.Up = ""
			err = decode(key, &cfg.Up)
		}
		if err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

// PinnedExtraKeys are the host-reaching keys that live in Extra rather than a
// named field. Each decides what a container is or runs, so like scaffold and
// env it must come from committed config, never the container-writable
// worktree: files mounts host paths in, node_image picks the image, and
// apt_packages/php_extensions/php_ini are spliced into the image's Dockerfile
// as root build steps.
var PinnedExtraKeys = []string{"files", "node_image", "apt_packages", "php_extensions", "php_ini"}

// LoadProjectForWorkspace prefers the worktree's slate.yml so a branch can
// test config changes. `project:` stays pinned to the main checkout, as do
// the host-executed `agent`/`up`: the worktree is container-writable, so
// honouring its copy would hand host execution to a rogue dependency.
func LoadProjectForWorkspace(mainRoot, wsDir string) (ProjectConfig, error) {
	mainCfg, err := LoadMainProject(mainRoot)
	if err != nil {
		return mainCfg, err
	}
	if wsDir == "" {
		return mainCfg, nil
	}

	cfg := mainCfg
	if info, err := os.Stat(filepath.Join(wsDir, "slate.yml")); err == nil && !info.IsDir() {
		wsCfg, err := LoadProject(wsDir)
		if err != nil {
			return wsCfg, fmt.Errorf("workspace slate.yml: %w", err)
		}
		wsCfg.Project = mainCfg.Project
		wsCfg.Agent = mainCfg.Agent
		wsCfg.New = mainCfg.New
		wsCfg.Up = mainCfg.Up
		wsCfg.Doctor = mainCfg.Doctor
		wsCfg.Brief = mainCfg.Brief
		cfg = wsCfg
	}

	// Host-reaching config never comes from the container-writable worktree
	// (see trustedConfig): scaffold and files directly, database and env
	// because they interpolate into compose files (env via the --env-file
	// that .env.container is passed as). Applied even when the worktree's
	// slate.yml is missing: container code can delete the file, and that must
	// not flip the trusted resolution away from the branch's committed config.
	trusted := trustedConfig(mainRoot, wsDir, mainCfg)
	cfg.Scaffold = trusted.Scaffold
	cfg.Database = trusted.Database
	cfg.Env = trusted.Env
	for _, key := range PinnedExtraKeys {
		val, ok := trusted.Extra[key]
		if cfg.Extra != nil {
			delete(cfg.Extra, key)
		}
		if !ok {
			continue
		}
		if cfg.Extra == nil {
			cfg.Extra = map[string]any{}
		}
		cfg.Extra[key] = val
	}
	return cfg, nil
}

// trustedConfig resolves the config whose keys can reach host resources
// (mount host files, define containers). Containers cannot commit (the main
// .git is mounted read-only), so committed content on the workspace branch is
// host-authored; failing that, the main checkout's slate.yml applies. The
// worktree's own working copy is never trusted for these keys: container code
// can rewrite it.
func trustedConfig(mainRoot, wsDir string, mainCfg ProjectConfig) ProjectConfig {
	data, ok := workspace.CommittedFile(mainRoot, wsDir, "slate.yml")
	if !ok {
		return mainCfg
	}
	var committed ProjectConfig
	if err := yaml.Unmarshal(data, &committed); err != nil {
		return mainCfg
	}
	return committed
}

// TrustPinned reports which host-reaching keys the workspace's working-tree
// slate.yml tries (inertly) to change relative to the trusted resolution.
func TrustPinned(mainRoot, wsDir string) []string {
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
	trusted := trustedConfig(mainRoot, wsDir, mainCfg)

	var pinned []string
	if !reflect.DeepEqual(wsCfg.Scaffold, trusted.Scaffold) {
		pinned = append(pinned, "scaffold")
	}
	if wsCfg.Database != trusted.Database {
		pinned = append(pinned, "database")
	}
	if !reflect.DeepEqual(wsCfg.Env, trusted.Env) {
		pinned = append(pinned, "env")
	}
	for _, key := range PinnedExtraKeys {
		if !reflect.DeepEqual(wsCfg.Extra[key], trusted.Extra[key]) {
			pinned = append(pinned, key)
		}
	}
	return pinned
}

// HostExecPinned reports which pinned host-exec fields the workspace's
// slate.yml tries (inertly) to change. Compared against the committed main
// slate.yml, not the slate.local.yml overlay: a worktree copy matching the
// committed config is no edit at all, and must not warn on every command
// just because a local overlay redirects the effective value.
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
	if wsCfg.New != "" && wsCfg.New != mainCfg.New {
		pinned = append(pinned, "new")
	}
	if wsCfg.Up != "" && wsCfg.Up != mainCfg.Up {
		pinned = append(pinned, "up")
	}
	if len(wsCfg.Doctor) > 0 && !reflect.DeepEqual(wsCfg.Doctor, mainCfg.Doctor) {
		pinned = append(pinned, "doctor")
	}
	if wsCfg.Brief != "" && wsCfg.Brief != mainCfg.Brief {
		pinned = append(pinned, "brief")
	}
	return pinned
}
