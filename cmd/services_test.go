package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceConfigNote(t *testing.T) {
	mainRoot := t.TempDir()
	wsDir := t.TempDir()
	write := func(dir, content string) {
		if err := os.WriteFile(filepath.Join(dir, "slate.yml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// no workspace copy: nothing to compare
	write(mainRoot, "scaffold: laravel\n")
	if note := workspaceConfigNote(mainRoot, wsDir); note != "" {
		t.Errorf("no ws slate.yml: want no note, got %q", note)
	}

	// identical (modulo trailing whitespace): quiet
	write(wsDir, "scaffold: laravel")
	if note := workspaceConfigNote(mainRoot, wsDir); note != "" {
		t.Errorf("identical configs: want no note, got %q", note)
	}

	// edited in the worktree only: warn, pointing at the file slate reads
	write(wsDir, "scaffold: laravel\nagent: claude\n")
	note := workspaceConfigNote(mainRoot, wsDir)
	if note == "" {
		t.Fatal("differing configs: want a note")
	}
	if want := filepath.Join(mainRoot, "slate.yml"); !strings.Contains(note, want) {
		t.Errorf("note should name %s, got %q", want, note)
	}
}
