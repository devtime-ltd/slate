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
