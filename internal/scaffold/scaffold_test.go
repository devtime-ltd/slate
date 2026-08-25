package scaffold

import (
	"embed"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/scaffolds"
)

func TestBuildLifecycleScriptUpOnly(t *testing.T) {
	cfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}}
	script := BuildLifecycleScript(cfg, false)

	if script == "" {
		t.Fatal("expected non-empty script")
	}
	if !strings.Contains(script, "composer install") {
		t.Error("up script should contain composer install")
	}
	if strings.Contains(script, "migrate:fresh") {
		t.Error("up-only script should not contain migrate:fresh")
	}
}

func TestBuildLifecycleScriptRetry(t *testing.T) {
	cfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}}
	script := BuildLifecycleScript(cfg, false)

	if !strings.Contains(script, "retry() {") {
		t.Error("script should define the retry shell helper")
	}
	if !strings.Contains(script, "retry composer install") {
		t.Error("composer install should be wrapped in retry")
	}
}

func TestBuildLifecycleScriptNewThenUp(t *testing.T) {
	cfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}}
	script := BuildLifecycleScript(cfg, true)

	if script == "" {
		t.Fatal("expected non-empty script")
	}

	composerIdx := strings.Index(script, "composer install")
	freshIdx := strings.Index(script, "migrate:fresh")

	if composerIdx < 0 {
		t.Fatal("should contain composer install")
	}
	if freshIdx < 0 {
		t.Fatal("should contain migrate:fresh")
	}
	if composerIdx > freshIdx {
		t.Error("composer install (from setup) should come before migrate:fresh (from fresh)")
	}
}

func TestBuildLifecycleScriptScaffoldPlaceholder(t *testing.T) {
	cfg := config.ProjectConfig{
		Scaffold: config.ScaffoldRef{Name: "laravel"},
		Setup:    "echo before\n{{SCAFFOLD_DEFAULT}}\necho after\n",
	}
	script := BuildLifecycleScript(cfg, false)

	if !strings.Contains(script, "echo before") {
		t.Error("should contain pre-scaffold content")
	}
	if !strings.Contains(script, "composer install") {
		t.Error("{{SCAFFOLD_DEFAULT}} should be expanded to default setup script")
	}
	if !strings.Contains(script, "echo after") {
		t.Error("should contain post-scaffold content")
	}
}

func TestBuildLifecycleScriptFullOverride(t *testing.T) {
	cfg := config.ProjectConfig{
		Scaffold: config.ScaffoldRef{Name: "laravel"},
		Setup:    "custom-command\n",
	}
	script := BuildLifecycleScript(cfg, false)

	if !strings.Contains(script, "custom-command") {
		t.Error("should contain custom command")
	}
	if strings.Contains(script, "composer install") {
		t.Error("full override should not contain scaffold defaults")
	}
}

func TestBuildLifecycleScriptNoScaffold(t *testing.T) {
	cfg := config.ProjectConfig{}
	script := BuildLifecycleScript(cfg, false)

	if script != "" {
		t.Errorf("no scaffold should produce empty script, got %q", script)
	}
}

func TestBuildLifecycleScriptNoneScaffold(t *testing.T) {
	cfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "none"}}
	script := BuildLifecycleScript(cfg, false)

	if script != "" {
		t.Errorf("none scaffold should produce empty script, got %q", script)
	}
}

func TestGetRegisteredScaffolds(t *testing.T) {
	s, err := Get("laravel")
	if err != nil {
		t.Fatalf("Get(laravel) failed: %v", err)
	}
	if s.Name() != "laravel" {
		t.Errorf("Name() = %q, want laravel", s.Name())
	}

	s, err = Get("nextjs")
	if err != nil {
		t.Fatalf("Get(nextjs) failed: %v", err)
	}
	if s.Name() != "nextjs" {
		t.Errorf("Name() = %q, want nextjs", s.Name())
	}
}

func TestGetUnknownScaffold(t *testing.T) {
	_, err := Get("unknown")
	if err == nil {
		t.Error("Get(unknown) should return error")
	}
	if !strings.Contains(err.Error(), "unknown scaffold") {
		t.Errorf("error = %q, should mention 'unknown scaffold'", err.Error())
	}
}

func TestGetNoneScaffold(t *testing.T) {
	_, err := Get("none")
	if err == nil {
		t.Error("Get(none) should return error (none is handled by Generate, not registry)")
	}
}

func TestDeriveDBName(t *testing.T) {
	tests := []struct {
		name      string
		project   string
		workspace string
		label     string
		wantRe    string
	}{
		{"default label", "sparta-bravo", "foo", "default", `^foo_[a-f0-9]{6}$`},
		{"named label", "sparta-bravo", "foo", "analytics", `^foo_analytics_[a-f0-9]{6}$`},
		{"hyphens sanitised", "proj", "fix-phpunit", "default", `^fix_phpunit_[a-f0-9]{6}$`},
		{"long workspace truncated", "proj", "redesign-festival-submission-flow", "default", `^redesign_festival_[a-f0-9]{6}$`},
		{"long label truncated", "proj", "foo", "user-generated-content-archive", `^foo_user_generated_con_[a-f0-9]{6}$`},
		{"both long", "proj", "redesign-festival-submission-flow", "user-generated-content-archive", `^redesign_festival_user_generated_con_[a-f0-9]{6}$`},
		{"leading digit gets x prefix", "proj", "2fa-fix", "default", `^x_2fa_fix_[a-f0-9]{6}$`},
		{"no double underscores", "proj", "fix--double", "default", `^fix_double_[a-f0-9]{6}$`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DeriveDBName(tt.project, tt.workspace, tt.label)
			re := regexp.MustCompile(tt.wantRe)
			if !re.MatchString(result) {
				t.Errorf("DeriveDBName(%q, %q, %q) = %q, want match %s", tt.project, tt.workspace, tt.label, result, tt.wantRe)
			}
			if len(result) > 50 {
				t.Errorf("result %q is %d chars, want <= 50", result, len(result))
			}
		})
	}
}

func TestDeriveDBNameDifferentProjects(t *testing.T) {
	a := DeriveDBName("project-a", "foo", "default")
	b := DeriveDBName("project-b", "foo", "default")
	if a == b {
		t.Errorf("different projects should produce different names: %q vs %q", a, b)
	}
}

func TestExpandDBNameInValue(t *testing.T) {
	result := expandDBName("postgresql://app:pass@host:5432/{{DB_NAME:default}}", "proj", "ws")
	if !strings.Contains(result, "postgresql://app:pass@host:5432/ws_") {
		t.Errorf("got %q, expected URL with expanded DB name", result)
	}
}

func TestExpandDBNameNoArg(t *testing.T) {
	result := expandDBName("{{DB_NAME}}", "proj", "ws")
	if !strings.HasPrefix(result, "ws_") {
		t.Errorf("got %q, expected ws_ prefix", result)
	}
}

func TestLaravelTools(t *testing.T) {
	s, _ := Get("laravel")
	tools := s.Tools()
	for _, name := range []string{"composer", "artisan", "pint", "pest", "npm", "npx", "tinker"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("laravel should have %s tool", name)
		}
	}
}

func TestLaravelDBTool(t *testing.T) {
	s, _ := Get("laravel")
	tool, ok := s.Tools()["mysql"]
	if !ok {
		t.Fatal("laravel should have mysql tool")
	}
	db, ok := tool.(config.DBTool)
	if !ok {
		t.Fatalf("mysql tool should be DBTool, got %T", tool)
	}
	if db.Scheme != "mysql" || db.User != "root" || db.Port != 3306 {
		t.Errorf("unexpected mysql tool: %+v", db)
	}
}

func TestNextjsDBTool(t *testing.T) {
	s, _ := Get("nextjs")
	tool, ok := s.Tools()["psql"]
	if !ok {
		t.Fatal("nextjs should have psql tool")
	}
	db, ok := tool.(config.DBTool)
	if !ok {
		t.Fatalf("psql tool should be DBTool, got %T", tool)
	}
	if db.Scheme != "postgresql" || db.User != "app" || db.Port != 5432 {
		t.Errorf("unexpected psql tool: %+v", db)
	}
}

func TestLaravelDefaultEnv(t *testing.T) {
	s, _ := Get("laravel")
	globalCfg := config.WithPorts(80, 443, true)
	env := s.DefaultEnv("proj--feat", globalCfg)

	if env["DB_CONNECTION"] != "mysql" {
		t.Errorf("DB_CONNECTION = %q, want mysql", env["DB_CONNECTION"])
	}
	if env["DB_HOST"] != "mysql" {
		t.Errorf("DB_HOST = %q, want mysql", env["DB_HOST"])
	}
	if env["APP_URL"] != "https://proj--feat.test" {
		t.Errorf("APP_URL = %q", env["APP_URL"])
	}
}

func TestNextjsDefaultEnv(t *testing.T) {
	s, _ := Get("nextjs")
	globalCfg := config.WithPorts(80, 443, true)
	env := s.DefaultEnv("proj--feat", globalCfg)

	if _, ok := env["DB_CONNECTION"]; ok {
		t.Error("nextjs should not have DB_CONNECTION (uses DATABASE_URL)")
	}
	if !strings.Contains(env["DATABASE_URL"], "postgresql://") {
		t.Errorf("DATABASE_URL = %q, should contain postgresql://", env["DATABASE_URL"])
	}
}

func TestLaravelDockerfileBakesPHPDefaults(t *testing.T) {
	s, err := Get("laravel")
	if err != nil {
		t.Fatalf("Get(laravel) failed: %v", err)
	}
	out, err := s.(embeddedScaffold).RenderDockerfile("WORKDIR /app\nUSER www-data\n", config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/usr/local/etc/php/conf.d/10-slate-defaults.ini") {
		t.Errorf("default php.ini drop-in missing from Dockerfile:\n%s", out)
	}
	if !strings.Contains(out, "'memory_limit' '512M'") {
		t.Errorf("default memory_limit not set:\n%s", out)
	}
}

func TestLaravelDockerfilePHPIniOverride(t *testing.T) {
	s, err := Get("laravel")
	if err != nil {
		t.Fatalf("Get(laravel) failed: %v", err)
	}
	cfg := config.ProjectConfig{
		Scaffold: config.ScaffoldRef{Name: "laravel"},
		Extra: map[string]any{
			"php_ini": map[string]any{
				"memory_limit":       "2G",
				"max_execution_time": 0,
			},
		},
	}
	out, err := s.(embeddedScaffold).RenderDockerfile("WORKDIR /app\nUSER www-data\n", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/usr/local/etc/php/conf.d/50-slate-yml.ini") {
		t.Errorf("user php.ini drop-in missing:\n%s", out)
	}
	if !strings.Contains(out, "'memory_limit' '2G'") {
		t.Errorf("user memory_limit override missing:\n%s", out)
	}
	if !strings.Contains(out, "'max_execution_time' '0'") {
		t.Errorf("unquoted int php_ini value should be stringified:\n%s", out)
	}
}

func TestLaravelDockerfileDoesNotImposeMaxExecutionTime(t *testing.T) {
	s, err := Get("laravel")
	if err != nil {
		t.Fatalf("Get(laravel) failed: %v", err)
	}
	out, err := s.(embeddedScaffold).RenderDockerfile("WORKDIR /app\n", config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "max_execution_time") {
		t.Errorf("default ini should not set max_execution_time (let PHP CLI default of 0 apply):\n%s", out)
	}
}

func TestRenderPHPIniDropEscapesMaliciousKey(t *testing.T) {
	out := renderPHPIniDrop("test.ini", map[string]string{
		"evil'; touch /tmp/pwned; echo 'foo": "x",
	})
	// The key's single quote should be escaped via the '\'' idiom so the
	// shell sees the dangerous payload as quoted text, not as separators.
	want := `'evil'\''; touch /tmp/pwned; echo '\''foo'`
	if !strings.Contains(out, want) {
		t.Errorf("malicious key not escape-quoted; want %q in:\n%s", want, out)
	}
}

func TestGenerateFileMountsAppTargetOnlyOnAppService(t *testing.T) {
	ws, src := newWorkspaceWithSourceFile(t, "x")
	cfg := config.ProjectConfig{Extra: map[string]any{
		"files": map[string]any{src: "/app/.slate/composer/auth.json"},
	}}
	if err := GenerateFileMounts(ws, cfg, &nullScaffold{}, []string{"app", "queue"}); err != nil {
		t.Fatal(err)
	}

	override, err := os.ReadFile(filepath.Join(ws, ".slate/compose.files.yaml"))
	if err != nil {
		t.Fatalf("expected compose.files.yaml: %v", err)
	}
	out := string(override)

	if !strings.Contains(out, "/app/.slate/composer/auth.json:ro") {
		t.Errorf("missing /app target mount entry:\n%s", out)
	}
	if strings.Contains(out, "queue:") {
		t.Errorf("/app target mount must not be declared on queue (races on host mountpoint):\n%s", out)
	}
}

func TestGenerateFileMountsNonAppTargetSharedAcrossServices(t *testing.T) {
	ws, src := newWorkspaceWithSourceFile(t, "x")
	cfg := config.ProjectConfig{Extra: map[string]any{
		"files": map[string]any{src: "/etc/something/file"},
	}}
	if err := GenerateFileMounts(ws, cfg, &nullScaffold{}, []string{"app", "queue"}); err != nil {
		t.Fatal(err)
	}

	override, err := os.ReadFile(filepath.Join(ws, ".slate/compose.files.yaml"))
	if err != nil {
		t.Fatalf("expected compose.files.yaml: %v", err)
	}
	out := string(override)
	if !strings.Contains(out, ":/etc/something/file:ro") {
		t.Errorf("expected bind mount entry, got:\n%s", out)
	}
	if !strings.Contains(out, "queue:") {
		t.Errorf("non-/app target should also appear on queue:\n%s", out)
	}
}

func TestGenerateFileMountsAppTargetDoesNotWriteToWorkspace(t *testing.T) {
	ws, src := newWorkspaceWithSourceFile(t, "secret-token")
	cfg := config.ProjectConfig{Extra: map[string]any{
		"files": map[string]any{src: "/app/.npmrc"},
	}}
	if err := GenerateFileMounts(ws, cfg, &nullScaffold{}, []string{"app", "queue"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".npmrc")); !os.IsNotExist(err) {
		t.Errorf("/app target must not be materialised in the worktree (would expose credentials to git status); err=%v", err)
	}
}

func TestGenerateFileMountsRefusesSymlinkedSlateDir(t *testing.T) {
	ws, src := newWorkspaceWithSourceFile(t, "x")
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(ws, ".slate")); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(target, "compose.files.yaml")
	cfg := config.ProjectConfig{Extra: map[string]any{
		"files": map[string]any{src: "/etc/foo"},
	}}
	if err := GenerateFileMounts(ws, cfg, &nullScaffold{}, []string{"app", "queue"}); err == nil {
		t.Error("a symlinked .slate must be refused")
	}
	if _, err := os.Lstat(victim); err == nil {
		t.Error("write reached the symlink target dir")
	}
}

func TestGenerateFileMountsRefusesSymlinkedFilesDir(t *testing.T) {
	ws, src := newWorkspaceWithSourceFile(t, "x")
	if err := os.MkdirAll(filepath.Join(ws, ".slate"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(ws, ".slate/files")); err != nil {
		t.Fatal(err)
	}
	cfg := config.ProjectConfig{Extra: map[string]any{
		"files": map[string]any{src: "/etc/foo"},
	}}
	if err := GenerateFileMounts(ws, cfg, &nullScaffold{}, []string{"app", "queue"}); err != nil {
		t.Fatalf("a symlinked .slate/files should be replaced, not error: %v", err)
	}
	// the symlink was removed (not followed) and a real dir created in its place
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Error("write reached the symlink target dir")
	}
	if info, err := os.Lstat(filepath.Join(ws, ".slate/files")); err != nil || !info.IsDir() {
		t.Errorf(".slate/files should be a real directory now: %v", err)
	}
}

func TestGenerateFileMountsClearsSymlinkedFilePayload(t *testing.T) {
	ws, src := newWorkspaceWithSourceFile(t, "fresh-content")
	filesDir := filepath.Join(ws, ".slate/files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideTarget := filepath.Join(t.TempDir(), "outside-victim")
	if err := os.WriteFile(outsideTarget, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideTarget, filepath.Join(filesDir, "file_0")); err != nil {
		t.Fatal(err)
	}

	cfg := config.ProjectConfig{Extra: map[string]any{
		"files": map[string]any{src: "/etc/foo"},
	}}
	if err := GenerateFileMounts(ws, cfg, &nullScaffold{}, []string{"app", "queue"}); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(outsideTarget)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "original" {
		t.Errorf("symlinked file_0 leaked write to outside target; got %q, want %q", written, "original")
	}
}

func TestGenerateFileMountsErrorsWhenScaffoldHasNoAppLikeServices(t *testing.T) {
	ws, src := newWorkspaceWithSourceFile(t, "x")
	cfg := config.ProjectConfig{Extra: map[string]any{
		"files": map[string]any{src: "/etc/foo"},
	}}
	if err := GenerateFileMounts(ws, cfg, &noAppServicesScaffold{}, nil); err == nil {
		t.Error("expected error when scaffold has no AppLikeServices() but files configured")
	}
}

func TestGenerateFileMountsScaffoldWithoutQueue(t *testing.T) {
	ws, src := newWorkspaceWithSourceFile(t, "x")
	cfg := config.ProjectConfig{Extra: map[string]any{
		"files": map[string]any{src: "/etc/something/file"},
	}}
	if err := GenerateFileMounts(ws, cfg, &appOnlyScaffold{}, []string{"app"}); err != nil {
		t.Fatal(err)
	}
	override, err := os.ReadFile(filepath.Join(ws, ".slate/compose.files.yaml"))
	if err != nil {
		t.Fatalf("expected compose.files.yaml: %v", err)
	}
	if strings.Contains(string(override), "queue:") {
		t.Errorf("scaffold without queue should not emit queue section:\n%s", override)
	}
}

func TestGenerateFileMountsClearsStaleOverride(t *testing.T) {
	ws, _ := newWorkspaceWithSourceFile(t, "x")
	stale := filepath.Join(ws, ".slate/compose.files.yaml")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale: true"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := GenerateFileMounts(ws, config.ProjectConfig{}, &nullScaffold{}, []string{"app", "queue"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale compose.files.yaml should be removed when no mounts remain (err=%v)", err)
	}
}

func newWorkspaceWithSourceFile(t *testing.T, content string) (string, string) {
	t.Helper()
	ws := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "source")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return ws, src
}

type nullScaffold struct{}

func (nullScaffold) Name() string                     { return "null" }
func (nullScaffold) FS() embed.FS                     { return embed.FS{} }
func (nullScaffold) FileMap(string) map[string]string { return nil }
func (nullScaffold) RenderDockerfile(c string, _ config.ProjectConfig) (string, error) {
	return c, nil
}
func (nullScaffold) DefaultFiles() map[string]string                          { return nil }
func (nullScaffold) DefaultEnv(string, config.GlobalConfig) map[string]string { return nil }
func (nullScaffold) Tools() map[string]config.Tool                            { return nil }
func (nullScaffold) Subdomains() map[string]Subdomain                         { return nil }
func (nullScaffold) AppLikeServices() []string                                { return []string{"app", "queue"} }

type appOnlyScaffold struct{ nullScaffold }

func (appOnlyScaffold) AppLikeServices() []string { return []string{"app"} }

type noAppServicesScaffold struct{ nullScaffold }

func (noAppServicesScaffold) AppLikeServices() []string { return nil }

func TestEnsureGitignoreAddsBothEntries(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureGitignore(dir); err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	for _, want := range []string{".slate/", ".env.container"} {
		if !hasGitignoreLine(string(data), want) {
			t.Errorf("expected .gitignore to contain %q, got:\n%s", want, data)
		}
	}
}

// Regression: a project that already ignores .slate/ (e.g. set up by an older
// slate) must still get .env.container added, rather than returning early.
func TestEnsureGitignoreAddsEnvContainerWhenSlateAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	gi := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gi, []byte("/vendor\n.slate/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(dir); err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	data, _ := os.ReadFile(gi)
	if !hasGitignoreLine(string(data), ".env.container") {
		t.Errorf("expected .env.container to be added, got:\n%s", data)
	}
}

func TestEnsureGitignoreIdempotent(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := EnsureGitignore(dir); err != nil {
			t.Fatalf("EnsureGitignore #%d: %v", i, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if got := strings.Count(string(data), ".env.container"); got != 1 {
		t.Errorf("expected .env.container exactly once, got %d:\n%s", got, data)
	}
	if got := strings.Count(string(data), ".slate/"); got != 1 {
		t.Errorf("expected .slate/ exactly once, got %d:\n%s", got, data)
	}
}

func hasGitignoreLine(content, want string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func composeFor(t *testing.T, mainRoot string) string {
	t.Helper()
	ws := t.TempDir()
	if err := Generate(ws, mainRoot, config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}}, Identity{Project: "proj", Workspace: "ws", Hostname: "proj--ws"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws, ".slate", "compose.yaml"))
	if err != nil {
		t.Fatalf("reading compose.yaml: %v", err)
	}
	return string(data)
}

func TestGenerateOmitsEnvMountWithoutRootEnv(t *testing.T) {
	out := composeFor(t, t.TempDir()) // mainRoot has no .env
	if strings.Contains(out, "/run/main-env/.env") {
		t.Errorf("env mount must be omitted when root .env is absent (Docker would create it as a dir):\n%s", out)
	}
}

func TestGenerateOmitsEnvMountWhenRootEnvIsDir(t *testing.T) {
	mainRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(mainRoot, ".env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := composeFor(t, mainRoot); strings.Contains(out, "/run/main-env/.env") {
		t.Errorf("a leftover .env directory must count as absent:\n%s", out)
	}
}

func TestGenerateIncludesEnvMountWithRootEnv(t *testing.T) {
	mainRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(mainRoot, ".env"), []byte("APP_NAME=hydra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := composeFor(t, mainRoot); !strings.Contains(out, "/run/main-env/.env") {
		t.Errorf("env mount must be present when root .env exists:\n%s", out)
	}
}

func envContainerFor(t *testing.T, mainRoot string) string {
	t.Helper()
	ws := t.TempDir()
	g := config.WithPorts(80, 443, true)
	g.SecretKey = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	cfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}}
	if err := GenerateEnvContainer(ws, mainRoot, "hydra--feat", "hydra", "feat", cfg, g); err != nil {
		t.Fatalf("GenerateEnvContainer: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws, ".env.container"))
	if err != nil {
		t.Fatalf("reading .env.container: %v", err)
	}
	return string(data)
}

func TestGenerateEnvContainerDerivesAppKeyWithoutRootEnv(t *testing.T) {
	out := envContainerFor(t, t.TempDir()) // no root .env
	if !strings.Contains(out, "APP_KEY=base64:") {
		t.Errorf("a stable APP_KEY must be generated when the project has no root .env:\n%s", out)
	}
	if strings.Contains(out, "{{GEN_APP_KEY}}") {
		t.Errorf("APP_KEY placeholder left unexpanded:\n%s", out)
	}
}

func TestGenerateEnvContainerKeepsProjectAppKey(t *testing.T) {
	mainRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(mainRoot, ".env"), []byte("APP_KEY=base64:realprojectkey\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := envContainerFor(t, mainRoot); strings.Contains(out, "APP_KEY=") {
		t.Errorf(".env.container must not override the project's own APP_KEY:\n%s", out)
	}
}

func renderLaravelCompose(t *testing.T, cfg config.ProjectConfig) string {
	t.Helper()
	raw, err := scaffolds.Laravel.ReadFile("laravel/compose.yaml.tmpl")
	if err != nil {
		t.Fatalf("reading embedded laravel compose: %v", err)
	}
	out, err := renderCompose(string(raw), "", cfg, Identity{Project: "p", Workspace: "w", Hostname: "p--w.test"})
	if err != nil {
		t.Fatalf("renderCompose failed: %v", err)
	}
	return out
}

func TestLaravelComposeDefaultsNodeImage(t *testing.T) {
	out := renderLaravelCompose(t, config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}})
	if !strings.Contains(out, `image: "`+defaultNodeImage+`"`+"\n") {
		t.Errorf("vite service should default to %s:\n%s", defaultNodeImage, out)
	}
}

func TestLaravelComposeNodeImageOverride(t *testing.T) {
	cfg := config.ProjectConfig{
		Scaffold: config.ScaffoldRef{Name: "laravel"},
		Extra:    map[string]any{"node_image": "node:22"},
	}
	out := renderLaravelCompose(t, cfg)
	if !strings.Contains(out, `image: "node:22"`+"\n") {
		t.Errorf("node_image not applied to vite service:\n%s", out)
	}
	if strings.Contains(out, `image: "`+defaultNodeImage+`"`+"\n") {
		t.Errorf("default node image should be gone when overridden:\n%s", out)
	}
}

func TestNextjsDockerfileNodeImageOverride(t *testing.T) {
	s, err := Get("nextjs")
	if err != nil {
		t.Fatalf("Get(nextjs) failed: %v", err)
	}
	raw, err := scaffolds.NextJS.ReadFile("nextjs/Dockerfile.tmpl")
	if err != nil {
		t.Fatalf("reading embedded nextjs Dockerfile: %v", err)
	}
	if !strings.HasPrefix(string(raw), "FROM "+nextjsNodeImage+"\n") {
		t.Fatalf("nextjsNodeImage must match the template's FROM line, else the override no-ops")
	}
	base := "FROM " + nextjsNodeImage + "\n\nRUN corepack enable\n"

	unset, err := s.(embeddedScaffold).RenderDockerfile(base, config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "nextjs"}})
	if err != nil {
		t.Fatal(err)
	}
	if unset != base {
		t.Errorf("Dockerfile should be untouched without node_image:\n%s", unset)
	}

	cfg := config.ProjectConfig{
		Scaffold: config.ScaffoldRef{Name: "nextjs"},
		Extra:    map[string]any{"node_image": "node:22-slim"},
	}
	out, err := s.(embeddedScaffold).RenderDockerfile(base, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "FROM node:22-slim\n") {
		t.Errorf("node_image not applied to FROM line:\n%s", out)
	}
	if !strings.Contains(out, "RUN corepack enable") {
		t.Errorf("rest of the Dockerfile should survive:\n%s", out)
	}
}

func TestLaravelComposeRejectsInjectingNodeImage(t *testing.T) {
	raw, err := scaffolds.Laravel.ReadFile("laravel/compose.yaml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.ProjectConfig{
		Scaffold: config.ScaffoldRef{Name: "laravel"},
		Extra: map[string]any{
			"node_image": "node:24\n    volumes:\n      - ${HOME}/.ssh:/loot",
		},
	}
	if _, err := renderCompose(string(raw), "", cfg, Identity{Project: "p", Workspace: "w"}); err == nil {
		t.Fatal("a node_image carrying newlines must be rejected, not rendered into compose")
	}
}

func TestNodeImageRejectsUnsafeValues(t *testing.T) {
	for _, bad := range []string{
		"node:24\nfoo: bar",
		"node:24 extra",
		`node:24"`,
		"${EVIL}",
		"-node:24",
	} {
		cfg := config.ProjectConfig{Extra: map[string]any{"node_image": bad}}
		if _, err := nodeImage(cfg, defaultNodeImage); err == nil {
			t.Errorf("node_image %q should be rejected", bad)
		}
	}
	for _, ok := range []string{
		"node:24",
		"node:24-slim",
		"registry.example.com:5000/team/node:24",
		"[2001:db8::1]:5000/team/node:24",
		"registry.example.com:5000/" + strings.Repeat("a", 255) + ":sometag",
		"node@sha256:" + strings.Repeat("a", 64),
	} {
		cfg := config.ProjectConfig{Extra: map[string]any{"node_image": ok}}
		if got, err := nodeImage(cfg, defaultNodeImage); err != nil || got != ok {
			t.Errorf("node_image %q should be accepted, got %q err=%v", ok, got, err)
		}
	}
}

func TestGenerateRenderErrorWritesNoGeneratedFiles(t *testing.T) {
	ws, mainRoot := t.TempDir(), t.TempDir()
	id := Identity{Project: "p", Workspace: "w", Hostname: "p--w"}

	if err := Generate(ws, mainRoot, config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}}, id); err != nil {
		t.Fatal(err)
	}
	dockerfile := filepath.Join(ws, ".slate", "Dockerfile")
	composeFile := filepath.Join(ws, ".slate", "compose.yaml")
	prevDockerfile, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	prevCompose, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatal(err)
	}

	// apt_packages changes the Dockerfile; the invalid node_image fails the
	// compose render, which happens after the Dockerfile render
	cfg := config.ProjectConfig{
		Scaffold: config.ScaffoldRef{Name: "laravel"},
		Extra: map[string]any{
			"apt_packages": []any{"ghostscript"},
			"node_image":   "not a valid image",
		},
	}
	if err := Generate(ws, mainRoot, cfg, id); err == nil {
		t.Fatal("an invalid node_image must fail the generate")
	}
	if got, _ := os.ReadFile(dockerfile); string(got) != string(prevDockerfile) {
		t.Error("a render error later in the file list must not have written the changed Dockerfile first")
	}
	if got, _ := os.ReadFile(composeFile); string(got) != string(prevCompose) {
		t.Error("compose.yaml must be untouched after a render error")
	}
}

func TestGenerateRefusesSymlinkedGeneratedFile(t *testing.T) {
	ws, mainRoot := t.TempDir(), t.TempDir()
	cfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "nextjs"}}
	id := Identity{Project: "p", Workspace: "w", Hostname: "p--w"}
	if err := Generate(ws, mainRoot, cfg, id); err != nil {
		t.Fatal(err)
	}

	// a hostile worktree plants a symlink where a generated file is written
	victim := filepath.Join(ws, "host-secret")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(ws, ".slate", "compose.yaml")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}

	if err := Generate(ws, mainRoot, cfg, id); err == nil {
		t.Error("Generate must refuse to write through a planted symlink")
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep me" {
		t.Errorf("the symlink target was clobbered: %q", got)
	}
}

func TestGenerateRefusesSymlinkedSlateDir(t *testing.T) {
	ws, mainRoot := t.TempDir(), t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(ws, ".slate")); err != nil {
		t.Fatal(err)
	}
	err := Generate(ws, mainRoot, config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "nextjs"}}, Identity{})
	if err == nil {
		t.Error("Generate must refuse a symlinked .slate")
	}
}

func TestNodeImageRejectsNonStringYAML(t *testing.T) {
	for _, raw := range []any{true, 24, 3.14} {
		cfg := config.ProjectConfig{Extra: map[string]any{"node_image": raw}}
		if _, err := nodeImage(cfg, defaultNodeImage); err == nil {
			t.Errorf("node_image %v (%T) should be rejected as non-string", raw, raw)
		}
	}
}
