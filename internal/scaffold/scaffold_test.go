package scaffold

import (
	"regexp"
	"strings"
	"testing"

	"github.com/devtime-ltd/slate/internal/config"
)

func TestBuildLifecycleScriptUpOnly(t *testing.T) {
	cfg := config.ProjectConfig{Scaffold: "laravel"}
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

func TestBuildLifecycleScriptNewThenUp(t *testing.T) {
	cfg := config.ProjectConfig{Scaffold: "laravel"}
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
		t.Error("composer install (from up) should come before migrate:fresh (from new)")
	}
}

func TestBuildLifecycleScriptScaffoldPlaceholder(t *testing.T) {
	cfg := config.ProjectConfig{
		Scaffold: "laravel",
		Up: "echo before\n{{SCAFFOLD_DEFAULT}}\necho after\n",
	}
	script := BuildLifecycleScript(cfg, false)

	if !strings.Contains(script, "echo before") {
		t.Error("should contain pre-scaffold content")
	}
	if !strings.Contains(script, "composer install") {
		t.Error("{{SCAFFOLD_DEFAULT}} should be expanded to default up script")
	}
	if !strings.Contains(script, "echo after") {
		t.Error("should contain post-scaffold content")
	}
}

func TestBuildLifecycleScriptFullOverride(t *testing.T) {
	cfg := config.ProjectConfig{
		Scaffold: "laravel",
		Up:       "custom-command\n",
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
	cfg := config.ProjectConfig{Scaffold: "none"}
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
	s, _ := Get("laravel")
	out, err := s.RenderDockerfile("WORKDIR /app\nUSER www-data\n", config.ProjectConfig{Scaffold: "laravel"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/usr/local/etc/php/conf.d/10-slate-defaults.ini") {
		t.Errorf("default php.ini drop-in missing from Dockerfile:\n%s", out)
	}
	if !strings.Contains(out, "memory_limit = 512M") {
		t.Errorf("default memory_limit not set:\n%s", out)
	}
}

func TestLaravelDockerfilePHPIniOverride(t *testing.T) {
	s, _ := Get("laravel")
	cfg := config.ProjectConfig{
		Scaffold: "laravel",
		Extra: map[string]any{
			"php_ini": map[string]any{
				"memory_limit":       "2G",
				"max_execution_time": 0,
			},
		},
	}
	out, err := s.RenderDockerfile("WORKDIR /app\nUSER www-data\n", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/usr/local/etc/php/conf.d/50-slate-yml.ini") {
		t.Errorf("user php.ini drop-in missing:\n%s", out)
	}
	if !strings.Contains(out, "memory_limit = 2G") {
		t.Errorf("user memory_limit override missing:\n%s", out)
	}
	if !strings.Contains(out, "max_execution_time = 0") {
		t.Errorf("unquoted int php_ini value should be stringified:\n%s", out)
	}
}

func TestLaravelDockerfileDoesNotImposeMaxExecutionTime(t *testing.T) {
	s, _ := Get("laravel")
	out, err := s.RenderDockerfile("WORKDIR /app\n", config.ProjectConfig{Scaffold: "laravel"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "max_execution_time") {
		t.Errorf("default ini should not set max_execution_time (let PHP CLI default of 0 apply):\n%s", out)
	}
}
