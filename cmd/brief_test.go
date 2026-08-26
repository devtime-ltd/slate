package cmd

import (
	"strings"
	"testing"

	"github.com/devtime-ltd/slate/internal/config"
)

func TestBriefTextLaravel(t *testing.T) {
	out := briefText(config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}})
	for _, want := range []string{"SLATE_WORKSPACE", "slate exec --", "slate artisan", `force="true"`} {
		if !strings.Contains(out, want) {
			t.Errorf("laravel brief missing %q:\n%s", want, out)
		}
	}
}

func TestBriefTextNoScaffold(t *testing.T) {
	out := briefText(config.ProjectConfig{})
	if strings.Contains(out, "phpunit") {
		t.Errorf("non-laravel brief should not carry the phpunit note:\n%s", out)
	}
	if !strings.Contains(out, "SLATE_WORKSPACE") {
		t.Errorf("brief missing workspace targeting guidance:\n%s", out)
	}
}

func TestBriefHookSection(t *testing.T) {
	dir := t.TempDir()

	// no workspace context: {{PROJECT}} expands, stdout lands under the heading
	got := briefHookSection(`echo "**Project:** {{PROJECT}}"`, "proj", "", dir)
	want := "\n## Project notes\n\n**Project:** proj\n"
	if got != want {
		t.Errorf("briefHookSection = %q, want %q", got, want)
	}

	// workspace context: workspace placeholders and env apply
	got = briefHookSection("echo {{WORKSPACE}} $SLATE_WORKSPACE", "proj", "ws1", dir)
	if !strings.Contains(got, "ws1 ws1") {
		t.Errorf("workspace context should expand {{WORKSPACE}} and set SLATE_WORKSPACE, got %q", got)
	}

	// failure warns (to stderr) and omits the section
	if got := briefHookSection("echo partial; exit 1", "proj", "", dir); got != "" {
		t.Errorf("a failing hook should omit the section, got %q", got)
	}

	// empty stdout: nothing to append
	if got := briefHookSection("true", "proj", "", dir); got != "" {
		t.Errorf("empty hook output should omit the section, got %q", got)
	}
}

func TestBriefHookDoesNotInheritWorkspaceEnv(t *testing.T) {
	t.Setenv("SLATE_WORKSPACE", "leaky")

	got := briefHookSection(`echo "[$SLATE_WORKSPACE]"`, "proj", "", t.TempDir())
	if !strings.Contains(got, "[]") {
		t.Errorf("without workspace context the hook must not inherit SLATE_WORKSPACE, got %q", got)
	}

	got = briefHookSection(`echo "[$SLATE_WORKSPACE]"`, "proj", "ws1", t.TempDir())
	if !strings.Contains(got, "[ws1]") {
		t.Errorf("workspace context should set SLATE_WORKSPACE over the inherited value, got %q", got)
	}
}
