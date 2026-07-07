package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultGlobal(t *testing.T) {
	cfg := DefaultGlobal()
	if cfg.HTTPPort != 80 {
		t.Errorf("HTTPPort = %d, want 80", cfg.HTTPPort)
	}
	if cfg.HTTPSPort != 443 {
		t.Errorf("HTTPSPort = %d, want 443", cfg.HTTPSPort)
	}
	if !cfg.TLS {
		t.Error("TLS should default to true")
	}
}

func TestLoadGlobalMissingFile(t *testing.T) {
	t.Setenv("SLATE_CONFIG_DIR", t.TempDir())
	cfg, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPSPort != 443 {
		t.Errorf("HTTPSPort = %d, want default 443", cfg.HTTPSPort)
	}
	if !cfg.TLS {
		t.Error("TLS should default to true when config missing")
	}
}

func TestLoadGlobalPartialYAML(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SLATE_CONFIG_DIR", dir)
	os.WriteFile(filepath.Join(dir, "config.yml"), []byte("https_port: 8443\n"), 0o644)

	cfg, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPSPort != 8443 {
		t.Errorf("HTTPSPort = %d, want 8443", cfg.HTTPSPort)
	}
	if cfg.HTTPPort != 80 {
		t.Errorf("HTTPPort = %d, want default 80", cfg.HTTPPort)
	}
	if !cfg.TLS {
		t.Error("TLS should remain true when not set in YAML")
	}
}

func TestLoadGlobalTLSFalse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SLATE_CONFIG_DIR", dir)
	os.WriteFile(filepath.Join(dir, "config.yml"), []byte("tls: false\n"), 0o644)

	cfg, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLS {
		t.Error("TLS should be false when explicitly set")
	}
}

func TestLoadProjectMissingFile(t *testing.T) {
	cfg, err := LoadProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppPort != 8080 {
		t.Errorf("AppPort = %d, want default 8080", cfg.AppPort)
	}
	if cfg.Scaffold != "" {
		t.Errorf("Scaffold = %q, want empty", cfg.Scaffold)
	}
}

func TestLoadProjectWithScaffold(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "slate.yml"), []byte("scaffold: laravel\n"), 0o644)

	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scaffold != "laravel" {
		t.Errorf("Scaffold = %q, want %q", cfg.Scaffold, "laravel")
	}
}

func TestLoadProjectExtraKeys(t *testing.T) {
	dir := t.TempDir()
	yaml := `scaffold: laravel
php_extensions: [imagick, redis]
apt_packages: [ghostscript]
custom_key: value
`
	os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(yaml), 0o644)

	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}

	exts := cfg.StringSlice("php_extensions")
	if len(exts) != 2 || exts[0] != "imagick" || exts[1] != "redis" {
		t.Errorf("php_extensions = %v, want [imagick redis]", exts)
	}

	pkgs := cfg.StringSlice("apt_packages")
	if len(pkgs) != 1 || pkgs[0] != "ghostscript" {
		t.Errorf("apt_packages = %v, want [ghostscript]", pkgs)
	}

	missing := cfg.StringSlice("nonexistent")
	if missing != nil {
		t.Errorf("nonexistent = %v, want nil", missing)
	}
}

func TestStringMap(t *testing.T) {
	dir := t.TempDir()
	yaml := `scaffold: laravel
files:
  ~/.composer/auth.json: /var/www/.composer/auth.json
  ~/.npmrc: /home/node/.npmrc
`
	os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(yaml), 0o644)

	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}

	files := cfg.StringMap("files")
	if len(files) != 2 {
		t.Fatalf("files has %d entries, want 2", len(files))
	}
	if files["~/.composer/auth.json"] != "/var/www/.composer/auth.json" {
		t.Error("composer auth.json mapping wrong")
	}
}

func TestLoadProjectLifecycleHooks(t *testing.T) {
	dir := t.TempDir()
	yaml := `scaffold: laravel
new: |
  php artisan migrate:fresh
up: |
  composer install
  {{scaffold}}
`
	os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(yaml), 0o644)

	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.New == "" {
		t.Error("New hook should be set")
	}
	if cfg.Up == "" {
		t.Error("Up hook should be set")
	}
}

func TestResolvedToolsOverride(t *testing.T) {
	cfg := ProjectConfig{
		Scaffold: "laravel",
		Tools: map[string]ExecTool{
			"mycommand": {Service: "app", Command: []string{"php", "script.php"}},
		},
	}
	tools := cfg.ResolvedTools()
	if _, ok := tools["mycommand"]; !ok {
		t.Error("custom tool should be present")
	}
}

func TestResolvedToolsEmpty(t *testing.T) {
	cfg := ProjectConfig{Scaffold: "laravel"}
	tools := cfg.ResolvedTools()
	if tools != nil {
		t.Errorf("ResolvedTools without override should return nil, got %v", tools)
	}
}

// The up hook runs on every `slate up`, so it must migrate without seeding;
// otherwise restarts re-run seeders and duplicate data. Seeding belongs only
// in the fresh hook (slate new / slate up --fresh).
func TestLaravelUpHookDoesNotSeed(t *testing.T) {
	up := DefaultSetupForScaffold("laravel")
	if strings.Contains(up, "--seed") || strings.Contains(up, "db:seed") {
		t.Errorf("laravel up hook must not seed (re-runs on every up), got:\n%s", up)
	}
	if !strings.Contains(up, "migrate") {
		t.Errorf("laravel up hook should still migrate, got:\n%s", up)
	}

	fresh := DefaultFreshSetupForScaffold("laravel")
	if !strings.Contains(fresh, "--seed") {
		t.Errorf("laravel fresh hook should seed, got:\n%s", fresh)
	}
}

func TestLoadProjectAgent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "slate.yml"), []byte("scaffold: laravel\nagent: claude\n"), 0o644)

	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AgentEnabled() {
		t.Error("AgentEnabled() = false, want true")
	}
	if got := cfg.ResolvedLanding(); got != "agent+shell" {
		t.Errorf("ResolvedLanding() = %q, want agent+shell", got)
	}
}

func TestLoadProjectAgentUnsupported(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "slate.yml"), []byte("agent: copilot\n"), 0o644)

	if _, err := LoadProject(dir); err == nil {
		t.Fatal("expected error for unsupported agent")
	}
}

func TestLoadProjectLandingValidation(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "slate.yml"), []byte("landing: sideways\n"), 0o644)
	if _, err := LoadProject(dir); err == nil {
		t.Fatal("expected error for invalid landing value")
	}

	// agent landings require an agent to be configured
	os.WriteFile(filepath.Join(dir, "slate.yml"), []byte("landing: agent\n"), 0o644)
	if _, err := LoadProject(dir); err == nil {
		t.Fatal("expected error for agent landing without agent")
	}

	os.WriteFile(filepath.Join(dir, "slate.yml"), []byte("agent: claude\nlanding: agent\n"), 0o644)
	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolvedLanding(); got != "agent" {
		t.Errorf("ResolvedLanding() = %q, want agent", got)
	}
}

func TestResolvedLandingDefaults(t *testing.T) {
	if got := (ProjectConfig{}).ResolvedLanding(); got != "shell" {
		t.Errorf("no agent: ResolvedLanding() = %q, want shell", got)
	}
	if got := (ProjectConfig{Agent: "none"}).ResolvedLanding(); got != "shell" {
		t.Errorf("agent none: ResolvedLanding() = %q, want shell", got)
	}
	cfg := ProjectConfig{Agent: "claude", Landing: "none"}
	if got := cfg.ResolvedLanding(); got != "none" {
		t.Errorf("explicit none: ResolvedLanding() = %q, want none", got)
	}
}

func TestLoadProjectForWorkspace(t *testing.T) {
	mainRoot := t.TempDir()
	wsDir := t.TempDir()
	os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: laravel\nproject: mainname\n"), 0o644)

	// no workspace slate.yml: main config applies
	cfg, err := LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentEnabled() || cfg.Project != "mainname" {
		t.Errorf("want main config, got %+v", cfg)
	}

	// workspace slate.yml wins, but project identity stays main's
	os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: laravel\nproject: renamed\nagent: claude\n"), 0o644)
	cfg, err = LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AgentEnabled() {
		t.Error("workspace agent setting should apply")
	}
	if cfg.Project != "mainname" {
		t.Errorf("project should stay pinned to main, got %q", cfg.Project)
	}

	// invalid workspace config errors rather than silently using main's
	os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("agent: copilot\n"), 0o644)
	if _, err := LoadProjectForWorkspace(mainRoot, wsDir); err == nil {
		t.Error("invalid workspace slate.yml should error")
	}
}
