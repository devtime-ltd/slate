package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devtime-ltd/slate/internal/config"
)

func TestGenerateSlateYmlInlineParses(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(generateSlateYml("inline")), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	def := cfg.Scaffold.Inline
	if def == nil {
		t.Fatal("init inline template should parse as an inline scaffold")
	}
	if def.Compose != "./slate/compose.yaml" {
		t.Errorf("Compose = %q", def.Compose)
	}
	if sp := def.Subdomains[""]; sp.Service != "app" || sp.Port != 8080 {
		t.Errorf("main subdomain = %+v", sp)
	}
}
