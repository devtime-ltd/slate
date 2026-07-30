package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitFixture(t *testing.T) string {
	t.Helper()
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, ".slate"), 0o755); err != nil {
		t.Fatal(err)
	}
	return wsDir
}

func writeLock(t *testing.T, wsDir string, pid int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wsDir, ".slate", "provisioning"), []byte(fmt.Sprintf("%d\n", pid)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAwaitProvisionNothingInFlight(t *testing.T) {
	wsDir := waitFixture(t)
	if err := awaitProvision(wsDir, 0); err != nil {
		t.Errorf("no lock: want nil, got %v", err)
	}

	// a stale .failed marker alone must not block exec-style callers
	if err := os.WriteFile(filepath.Join(wsDir, ".slate", "provisioning.failed"), []byte("123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := awaitProvision(wsDir, 0); err != nil {
		t.Errorf("stale .failed without a live lock: want nil, got %v", err)
	}
}

func TestAwaitProvisionDeadProvisioner(t *testing.T) {
	wsDir := waitFixture(t)
	writeLock(t, wsDir, 999999999)
	if err := os.WriteFile(provisionLogPath(wsDir), []byte("step one\nboom\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := awaitProvision(wsDir, 0)
	if err == nil {
		t.Fatal("dead pid lock: want error")
	}
	for _, want := range []string{"died", "boom", "slate up"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got %q", want, err)
		}
	}
}

func TestAwaitProvisionWatchesCompletion(t *testing.T) {
	lockPath := func(wsDir string) string { return filepath.Join(wsDir, ".slate", "provisioning") }

	t.Run("success", func(t *testing.T) {
		wsDir := waitFixture(t)
		writeLock(t, wsDir, os.Getpid())
		go func() {
			time.Sleep(100 * time.Millisecond)
			os.Remove(lockPath(wsDir))
		}()
		if err := awaitProvision(wsDir, 5*time.Second); err != nil {
			t.Errorf("completed provision: want nil, got %v", err)
		}
	})

	t.Run("failure", func(t *testing.T) {
		wsDir := waitFixture(t)
		writeLock(t, wsDir, os.Getpid())
		if err := os.WriteFile(provisionLogPath(wsDir), []byte("lifecycle failed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		go func() {
			time.Sleep(100 * time.Millisecond)
			os.Rename(lockPath(wsDir), filepath.Join(wsDir, ".slate", "provisioning.failed"))
		}()
		err := awaitProvision(wsDir, 5*time.Second)
		if err == nil {
			t.Fatal("failed provision: want error")
		}
		if !strings.Contains(err.Error(), "lifecycle failed") {
			t.Errorf("error should carry the log tail, got %q", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		wsDir := waitFixture(t)
		writeLock(t, wsDir, os.Getpid())
		err := awaitProvision(wsDir, 100*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "still provisioning") {
			t.Errorf("want timeout error, got %v", err)
		}
	})
}

func TestTailFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := tailFile(path, 15)
	if strings.Contains(got, "line 5\n") || !strings.HasPrefix(got, "line 6") || !strings.HasSuffix(got, "line 20") {
		t.Errorf("want last 15 lines (6..20), got %q", got)
	}
	if tailFile(filepath.Join(dir, "missing"), 5) != "" {
		t.Error("missing file: want empty tail")
	}

	// a log bigger than the read chunk still yields the true last lines
	big := filepath.Join(dir, "big")
	var sb strings.Builder
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&sb, "big line %d with some padding to grow the file quickly\n", i)
	}
	if err := os.WriteFile(big, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got = tailFile(big, 3)
	if !strings.HasSuffix(got, "big line 5000 with some padding to grow the file quickly") || len(strings.Split(got, "\n")) != 3 {
		t.Errorf("large file: want the 3 true last lines, got %q", got)
	}
}
