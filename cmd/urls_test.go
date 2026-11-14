package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/devtime-ltd/slate/internal/config"
)

func fakePorts(published map[string]string) func(string, int) string {
	return func(service string, _ int) string { return published[service] }
}

func TestPrintLinksIsOneTabSeparatedLinePerLink(t *testing.T) {
	var out bytes.Buffer
	printLinks(&out, "https://shop--ui.test", []serviceLink{{"mailpit", "https://mailpit.shop--ui.test"}, {"mysql", "shop--ui.test:33061"}})
	want := "main\thttps://shop--ui.test\n" +
		"mailpit\thttps://mailpit.shop--ui.test\n" +
		"mysql\tshop--ui.test:33061\n"
	if out.String() != want {
		t.Errorf("got\n%q\nwant\n%q", out.String(), want)
	}
}

func TestWorkspaceLinksForLaravelAreItsSubdomainsAndDatabaseSorted(t *testing.T) {
	global := config.GlobalConfig{TLS: true, HTTPSPort: 443}
	cfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}}
	main, links := workspaceLinks(fakePorts(map[string]string{"app": "1", "vite": "2", "mailpit": "3", "mysql": "33061"}), "shop--ui", cfg, global)
	if main != "https://shop--ui.test" {
		t.Errorf("main: %s", main)
	}
	want := []serviceLink{
		{"mailpit", "https://mailpit.shop--ui.test"},
		{"mysql", "shop--ui.test:33061"},
		{"vite", "https://vite.shop--ui.test"},
	}
	if !slices.Equal(links, want) {
		t.Errorf("links: %+v", links)
	}
}

func TestWorkspaceLinksForNextjsIncludePostgres(t *testing.T) {
	global := config.GlobalConfig{TLS: true, HTTPSPort: 443}
	cfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "nextjs"}}
	_, links := workspaceLinks(fakePorts(map[string]string{"app": "1", "mailpit": "3", "postgres": "54321"}), "shop--ui", cfg, global)
	want := []serviceLink{
		{"mailpit", "https://mailpit.shop--ui.test"},
		{"postgres", "shop--ui.test:54321"},
	}
	if !slices.Equal(links, want) {
		t.Errorf("links: %+v", links)
	}
}

func TestWorkspaceLinksSkipUnpublishedServices(t *testing.T) {
	global := config.GlobalConfig{TLS: true, HTTPSPort: 443}
	cfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}}
	_, links := workspaceLinks(fakePorts(map[string]string{"app": "1", "vite": "2"}), "shop--ui", cfg, global)
	if !slices.Equal(links, []serviceLink{{"vite", "https://vite.shop--ui.test"}}) {
		t.Errorf("links: %+v", links)
	}
}

func TestWorkspaceLinksForAnInlineScaffoldAreItsPublishedSubdomainsSorted(t *testing.T) {
	global := config.GlobalConfig{TLS: true, HTTPSPort: 8443}
	cfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Inline: &config.InlineScaffold{
		Subdomains: map[string]config.ServicePort{
			"":     {Service: "web", Port: 80},
			"docs": {Service: "docs", Port: 3000},
			"api":  {Service: "api", Port: 8080},
			"blog": {Service: "blog", Port: 4000},
		},
	}}}
	main, links := workspaceLinks(fakePorts(map[string]string{"web": "1", "docs": "2", "api": "3"}), "shop--ui", cfg, global)
	if main != "https://shop--ui.test:8443" {
		t.Errorf("main: %s", main)
	}
	want := []serviceLink{
		{"api", "https://api.shop--ui.test:8443"},
		{"docs", "https://docs.shop--ui.test:8443"},
	}
	if !slices.Equal(links, want) {
		t.Errorf("links: %+v", links)
	}
}

func TestLivePortForSkipsStoppedServices(t *testing.T) {
	portFor := livePortFor([]string{"app", "mysql"}, fakePorts(map[string]string{"vite": "5173", "mysql": "33061"}))
	if got := portFor("vite", 5173); got != "" {
		t.Errorf("stopped vite reported port %q", got)
	}
	if got := portFor("mysql", 3306); got != "33061" {
		t.Errorf("running mysql: %q", got)
	}
}

func TestWorkspaceConfigUsesTheWorkspaceBranchScaffold(t *testing.T) {
	mainRoot := t.TempDir()
	landedGit(t, mainRoot, "init", "-q", "-b", "main")
	landedGit(t, mainRoot, "config", "user.email", "test@example.com")
	landedGit(t, mainRoot, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: laravel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landedGit(t, mainRoot, "add", ".")
	landedGit(t, mainRoot, "commit", "-q", "-m", "init")
	wsDir := filepath.Join(mainRoot, ".slate", "workspaces", "feat")
	landedGit(t, mainRoot, "worktree", "add", "-q", "-b", "slate/feat", wsDir)
	if err := os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: nextjs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landedGit(t, wsDir, "commit", "-q", "-am", "switch scaffold")

	mainCfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}}
	cfg := workspaceConfig(mainRoot, wsDir, mainCfg)
	_, links := workspaceLinks(fakePorts(map[string]string{"postgres": "54321"}), "shop--feat", cfg, config.GlobalConfig{TLS: true, HTTPSPort: 443})
	if !slices.Equal(links, []serviceLink{{"postgres", "shop--feat.test:54321"}}) {
		t.Errorf("links: %+v", links)
	}
}

func TestWorkspaceConfigFallsBackToTheMainConfig(t *testing.T) {
	mainRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(mainRoot, "slate.yml"), []byte("scaffold: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainCfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}}
	if cfg := workspaceConfig(mainRoot, t.TempDir(), mainCfg); cfg.Scaffold.Name != "laravel" {
		t.Errorf("scaffold: %q", cfg.Scaffold.Name)
	}
}
