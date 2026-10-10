package cmd

import (
	"bytes"
	"testing"

	"github.com/devtime-ltd/slate/internal/config"
)

func fakePorts(published map[string]string) func(string, int) string {
	return func(service string, _ int) string { return published[service] }
}

func TestPrintLinksIsOneTabSeparatedLinePerLink(t *testing.T) {
	global := config.GlobalConfig{TLS: true, HTTPSPort: 443}
	cfg := config.ProjectConfig{VitePort: 5173}
	main, links := workspaceLinks(fakePorts(map[string]string{"vite": "5173", "mysql": "33061"}), "shop--ui", cfg, global)
	var out bytes.Buffer
	printLinks(&out, main, links)
	want := "main\thttps://shop--ui.test\n" +
		"vite\thttps://vite.shop--ui.test\n" +
		"mysql\tshop--ui.test:33061\n"
	if out.String() != want {
		t.Errorf("got\n%q\nwant\n%q", out.String(), want)
	}
}

func TestWorkspaceLinksListOnlyPublishedInlineSubdomains(t *testing.T) {
	global := config.GlobalConfig{TLS: true, HTTPSPort: 8443}
	cfg := config.ProjectConfig{Scaffold: config.ScaffoldRef{Inline: &config.InlineScaffold{
		Subdomains: map[string]config.ServicePort{
			"":     {Service: "web", Port: 80},
			"docs": {Service: "docs", Port: 3000},
			"api":  {Service: "api", Port: 8080},
		},
	}}}
	main, links := workspaceLinks(fakePorts(map[string]string{"web": "1", "docs": "2"}), "shop--ui", cfg, global)
	if main != "https://shop--ui.test:8443" {
		t.Errorf("main: %s", main)
	}
	if len(links) != 1 || links[0] != (serviceLink{"docs", "https://docs.shop--ui.test:8443"}) {
		t.Errorf("links: %+v", links)
	}
}
