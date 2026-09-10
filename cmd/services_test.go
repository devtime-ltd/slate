package cmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
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

	// edited in the worktree only: note which config wins
	write(wsDir, "scaffold: laravel\nagent: claude\n")
	note := workspaceConfigNote(mainRoot, wsDir)
	if note == "" {
		t.Fatal("differing configs: want a note")
	}
	if !strings.Contains(note, "workspace's slate.yml") {
		t.Errorf("note should say the workspace config is in use, got %q", note)
	}
}

func TestProvisioningLockCleanupTombstone(t *testing.T) {
	wsDir := t.TempDir()
	slateDir := filepath.Join(wsDir, ".slate")
	if err := os.MkdirAll(slateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cleanup := writeProvisioningLock(wsDir)

	// deny dir writes so the lock can't be unlinked, only rewritten in place
	if err := os.Chmod(slateDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(slateDir, 0o755) })

	cleanup(nil)
	if pid, alive := readProvisioningLock(wsDir); pid != 0 || alive {
		t.Errorf("tombstoned lock should read as no lock, got pid=%d alive=%v", pid, alive)
	}
	data, err := os.ReadFile(filepath.Join(slateDir, "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "0" {
		t.Errorf("want pid-0 tombstone, got %q", data)
	}
}

func TestWorkerServices(t *testing.T) {
	laravel := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}}
	if got := workerServices(compose.Env{}, laravel); len(got) != 1 || got[0] != "queue" {
		t.Errorf("laravel workers = %v, want [queue]", got)
	}
	nextjs := config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "nextjs"}}
	if got := workerServices(compose.Env{}, nextjs); got != nil {
		t.Errorf("nextjs workers = %v, want none", got)
	}
}

func TestLiveWorkers(t *testing.T) {
	workers := []string{"queue", "scheduler"}
	cases := []struct {
		name string
		live []string
		want []string
	}{
		{"all running", []string{"app", "queue", "scheduler", "postgres"}, []string{"queue", "scheduler"}},
		{"one stopped by hand stays stopped", []string{"app", "scheduler"}, []string{"scheduler"}},
		{"stack down", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveWorkers(workers, tc.live); !slices.Equal(got, tc.want) {
				t.Errorf("liveWorkers(%v, %v) = %v, want %v", workers, tc.live, got, tc.want)
			}
		})
	}
}

// The refusal comes before any compose call, so no docker is needed here.
func TestPauseWorkersRefusesTheTargetWorker(t *testing.T) {
	mainRoot := newTestProject(t)
	_, err := pauseWorkers(compose.Env{}, mainRoot, "queue")
	if err == nil || !strings.Contains(err.Error(), "queue") {
		t.Fatalf("pausing workers for a command in the queue should be refused, got %v", err)
	}
}

// Run in a child process: the assertion is that SIGINT kills it, which the
// hold from a real pause would prevent.
func TestPauseWorkersWithoutWorkersLeavesSIGINTAlone(t *testing.T) {
	if os.Getenv("SLATE_TEST_CHILD") == "1" {
		mainRoot := newTestProject(t)
		commitSlateYml(t, mainRoot, generateSlateYml("nextjs"))
		restore, err := pauseWorkers(compose.Env{}, mainRoot, "app")
		if err != nil {
			t.Fatal(err)
		}
		defer restore()
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestPauseWorkersWithoutWorkersLeavesSIGINTAlone$")
	child.Env = append(os.Environ(), "SLATE_TEST_CHILD=1")
	out, err := child.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || !exitErr.Sys().(syscall.WaitStatus).Signaled() {
		t.Fatalf("a pause with nothing to stop should leave SIGINT alone, child exited with %v\n%s", err, out)
	}
}

// the scaffold is read from the committed slate.yml, not the working copy
func commitSlateYml(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "slate.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "scaffold"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}
