package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devtime-ltd/slate/internal/config"
)

func inlineCfg(def *config.InlineScaffold) config.ProjectConfig {
	return config.ProjectConfig{Scaffold: config.ScaffoldRef{Inline: def}}
}

func TestResolveInline(t *testing.T) {
	cfg := inlineCfg(&config.InlineScaffold{
		Subdomains: map[string]config.ServicePort{
			"":       {Service: "app", Port: 8081},
			"warden": {Service: "warden", Port: 8080},
		},
	})
	s, err := Resolve(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "inline" {
		t.Errorf("Name() = %q, want inline", s.Name())
	}
	subs := s.Subdomains()
	if subs[""].Service != "app" || subs[""].Port != 8081 {
		t.Errorf("main subdomain = %+v", subs[""])
	}
	if subs["warden"].Service != "warden" || subs["warden"].Port != 8080 {
		t.Errorf("warden subdomain = %+v", subs["warden"])
	}
	if s.AppLikeServices() != nil {
		t.Error("inline scaffold should signal derive-from-compose with nil AppLikeServices")
	}
	if s.Tools() != nil {
		t.Error("inline scaffold has no built-in tools")
	}
}

func TestResolveEmptyAndNone(t *testing.T) {
	for _, ref := range []config.ScaffoldRef{{}, {Name: "none"}} {
		s, err := Resolve(config.ProjectConfig{Scaffold: ref})
		if err != nil {
			t.Fatalf("Resolve(%+v): %v", ref, err)
		}
		if s.Name() != "inline" {
			t.Errorf("Resolve(%+v).Name() = %q, want inline", ref, s.Name())
		}
		if len(s.Subdomains()) != 0 {
			t.Errorf("Resolve(%+v) should have no subdomains", ref)
		}
	}
}

func TestResolveBuiltinAndUnknown(t *testing.T) {
	s, err := Resolve(config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}})
	if err != nil || s.Name() != "laravel" {
		t.Errorf("Resolve(laravel) = %v, %v", s, err)
	}
	if _, err := Resolve(config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "nope"}}); err == nil {
		t.Error("Resolve(nope) should error")
	}
}

func TestGenerateInlineCopiesComposeFromMainCheckout(t *testing.T) {
	ws, main := t.TempDir(), t.TempDir()
	content := "services:\n  app: {}\n# not a template: {{ .Hostname }}\n"
	writeWorkspaceFile(t, main, "slate/compose.yaml", content)
	// a worktree copy must never be the source
	writeWorkspaceFile(t, ws, "slate/compose.yaml", "services:\n  evil: {}\n")

	cfg := inlineCfg(&config.InlineScaffold{Compose: "./slate/compose.yaml"})
	if err := Generate(ws, main, cfg, Identity{Hostname: "p--w"}); err != nil {
		t.Fatal(err)
	}

	got := readGenerated(t, ws)
	if got != content {
		t.Errorf("compose.yaml should come verbatim from the main checkout:\n%s", got)
	}
}

func TestGenerateInlineRendersTmpl(t *testing.T) {
	ws, main := t.TempDir(), t.TempDir()
	tmpl := "# {{.Hostname}} ({{.Project}}/{{.Workspace}})\n{{if .Vars.with_warden}}  warden: {}\n{{end}}"
	writeWorkspaceFile(t, main, "slate/compose.yaml.tmpl", tmpl)

	cfg := inlineCfg(&config.InlineScaffold{
		Compose: "./slate/compose.yaml.tmpl",
		Vars:    map[string]any{"with_warden": true},
	})
	id := Identity{Project: "proj", Workspace: "feat", Hostname: "proj--feat"}
	if err := Generate(ws, main, cfg, id); err != nil {
		t.Fatal(err)
	}

	got := readGenerated(t, ws)
	if !strings.Contains(got, "# proj--feat (proj/feat)") {
		t.Errorf("identity not rendered:\n%s", got)
	}
	if !strings.Contains(got, "warden: {}") {
		t.Errorf("Vars condition not rendered:\n%s", got)
	}
}

func TestGenerateInlineBranchCommitBeatsMainAndWorktree(t *testing.T) {
	main := t.TempDir()
	gitRun(t, main, "init", "-q", "-b", "main")
	writeWorkspaceFile(t, main, "slate/compose.yaml", "# from-main\n")
	gitRun(t, main, "add", ".")
	gitRun(t, main, "commit", "-q", "-m", "init")

	ws := filepath.Join(main, ".slate", "workspaces", "feat")
	gitRun(t, main, "worktree", "add", "-q", "-b", "slate/feat", ws)

	writeWorkspaceFile(t, ws, "slate/compose.yaml", "# from-branch\n")
	gitRun(t, ws, "add", "slate/compose.yaml")
	gitRun(t, ws, "commit", "-q", "-m", "branch compose")

	// neither an uncommitted worktree edit (container-writable) nor a newer
	// main working tree may override the branch's committed copy
	writeWorkspaceFile(t, ws, "slate/compose.yaml", "# tampered\n")
	writeWorkspaceFile(t, main, "slate/compose.yaml", "# main-newer\n")

	cfg := inlineCfg(&config.InlineScaffold{Compose: "./slate/compose.yaml"})
	if err := Generate(ws, main, cfg, Identity{}); err != nil {
		t.Fatal(err)
	}
	if got := readGenerated(t, ws); !strings.Contains(got, "# from-branch") {
		t.Errorf("branch-committed compose should win, got:\n%s", got)
	}
}

func TestGenerateInlineMissingComposeErrors(t *testing.T) {
	cfg := inlineCfg(&config.InlineScaffold{Compose: "./slate/compose.yaml"})
	err := Generate(t.TempDir(), t.TempDir(), cfg, Identity{})
	if err == nil || !strings.Contains(err.Error(), "commit it") {
		t.Errorf("missing compose file should error with a hint, got %v", err)
	}
}

func TestGenerateInlineNoComposeGeneratesNothing(t *testing.T) {
	for _, ref := range []config.ScaffoldRef{{Name: "none"}, {Inline: &config.InlineScaffold{}}} {
		ws := t.TempDir()
		if err := Generate(ws, t.TempDir(), config.ProjectConfig{Scaffold: ref}, Identity{}); err != nil {
			t.Fatalf("Generate(%+v): %v", ref, err)
		}
		if _, err := os.Stat(ComposeFilePath(ws)); !os.IsNotExist(err) {
			t.Errorf("Generate(%+v) should not write compose.yaml (err=%v)", ref, err)
		}
	}
}

func TestGenerateInlineRejectsEscapingComposePath(t *testing.T) {
	cfg := inlineCfg(&config.InlineScaffold{Compose: "../outside.yaml"})
	err := Generate(t.TempDir(), t.TempDir(), cfg, Identity{})
	if err == nil || !strings.Contains(err.Error(), "stay inside") {
		t.Errorf("escaping compose path should be rejected, got %v", err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=test@test", "-c", "user.name=test", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeWorkspaceFile(t *testing.T, ws, rel, content string) {
	t.Helper()
	path := filepath.Join(ws, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readGenerated(t *testing.T, ws string) string {
	t.Helper()
	data, err := os.ReadFile(ComposeFilePath(ws))
	if err != nil {
		t.Fatalf("reading generated compose.yaml: %v", err)
	}
	return string(data)
}
