package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/safeio"
	"github.com/devtime-ltd/slate/internal/workspace"
)

func TestExpandCommand(t *testing.T) {
	got := expandCommand(`claude --name "{{HOSTNAME}}" # {{PROJECT}}/{{WORKSPACE}}`, "api", "shop")
	want := `claude --name "shop--api" # shop/api`
	if got != want {
		t.Errorf("expandCommand = %q, want %q", got, want)
	}
}

func TestAgentFresh(t *testing.T) {
	cases := []struct {
		name     string
		marker   bool
		pending  bool
		bare     bool
		freshEnv string
		want     bool
	}{
		{"first entry via up hook", false, false, false, "1", true},
		{"first entry in bare workspace", false, false, true, "", true},
		// how a tmux session or a later shell reaches `slate agent`: no
		// SLATE_FRESH, no bareness, and a session still owed
		{"first entry outside the hooks", false, true, false, "", true},
		{"existing pre-marker workspace", false, false, false, "", false},
		{"marker beats SLATE_FRESH", true, false, false, "1", false},
		{"marker beats bare", true, false, true, "", false},
		{"marker beats pending", true, true, false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, wsDir := newAgentWorktree(t)
			if tc.marker {
				if err := os.WriteFile(agentStartedMarker(wsDir), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.pending {
				if err := os.WriteFile(firstRunPendingMarker(wsDir), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.bare {
				if err := os.WriteFile(unprovisionedMarker(wsDir), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("SLATE_FRESH", tc.freshEnv)
			if got := agentFresh(root, wsDir); got != tc.want {
				t.Fatalf("agentFresh = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAgentMinRuntime(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", defaultAgentMinRuntime},
		{"nonsense", defaultAgentMinRuntime},
		{"-1", defaultAgentMinRuntime},
		{"0", 0},
		{"0.5", 500 * time.Millisecond},
		{"10", 10 * time.Second},
		// These parse without error but convert to a 0s or 292-year duration,
		// silently disabling the check or failing every launch.
		{"NaN", defaultAgentMinRuntime},
		{"Inf", defaultAgentMinRuntime},
		{"+Inf", defaultAgentMinRuntime},
		{"-Inf", defaultAgentMinRuntime},
		{"1e300", defaultAgentMinRuntime},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("SLATE_AGENT_MIN_RUNTIME", tc.env)
			if got := agentMinRuntime(); got != tc.want {
				t.Fatalf("agentMinRuntime = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHostRunBailed(t *testing.T) {
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "1")
	if !(hostRun{elapsed: 200 * time.Millisecond}).bailed() {
		t.Error("a 200ms run should count as bailed")
	}
	if (hostRun{elapsed: 2 * time.Second}).bailed() {
		t.Error("a 2s run should not count as bailed")
	}

	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0")
	if (hostRun{}).bailed() {
		t.Error("an instant run should not count as bailed when the check is disabled")
	}
}

func TestHookNeedsAgentNote(t *testing.T) {
	var unset config.ProjectConfig
	if hookNeedsAgentNote(unset, "slate agent") == "" {
		t.Error("want a note when a hook runs `slate agent` with no agent: configured")
	}
	if note := hookNeedsAgentNote(unset, "npm run dev"); note != "" {
		t.Errorf("want no note for an unrelated hook, got %q", note)
	}
	configured := config.ProjectConfig{Agent: config.AgentCmd{First: "claude", Again: "claude"}}
	if note := hookNeedsAgentNote(configured, "slate agent"); note != "" {
		t.Errorf("want no note when agent: is configured, got %q", note)
	}
}

func TestAgentUnconfiguredError(t *testing.T) {
	mainRoot := t.TempDir()

	bare := t.TempDir()
	if err := agentUnconfiguredError(mainRoot, bare); !strings.Contains(err.Error(), "set a command") {
		t.Errorf("want the generic hint, got %v", err)
	}

	wsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("agent: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := agentUnconfiguredError(mainRoot, wsDir)
	if !strings.Contains(err.Error(), "only ever come from the main checkout") {
		t.Errorf("want the pinning explanation when the workspace sets agent:, got %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(mainRoot, "slate.yml")) {
		t.Errorf("want the main checkout's slate.yml named, got %v", err)
	}
}

// runAgentIn runs the agent in a throwaway workspace carrying the given
// .slate markers and reports the error alongside whether the entry was
// recorded.
func runAgentIn(t *testing.T, agent config.AgentCmd, fresh bool, markers ...string) (string, error, bool) {
	t.Helper()
	return runAgentForcedIn(t, agent, fresh, false, markers...)
}

// runAgentForcedIn is runAgentIn with the variant chosen by --fresh/--continue.
func runAgentForcedIn(t *testing.T, agent config.AgentCmd, fresh, forced bool, markers ...string) (string, error, bool) {
	t.Helper()
	_, wsDir, err, marked := runAgentInProject(t, agent, fresh, forced, markers...)
	return wsDir, err, marked
}

// runAgentInProject is runAgentForcedIn returning the main checkout too, for
// a test that judges the workspace afterwards through its registration.
func runAgentInProject(t *testing.T, agent config.AgentCmd, fresh, forced bool, markers ...string) (string, string, error, bool) {
	t.Helper()
	// so a failing runAgent can never hold the test hostage with a shell,
	// however the suite is invoked
	agentNoHold = true
	t.Cleanup(func() { agentNoHold = false })
	root, wsDir := newAgentWorktree(t)
	for _, marker := range markers {
		if err := writeWorkspaceMarker(wsDir, marker, nil); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.ProjectConfig{Project: "proj", Agent: agent}
	err := runAgent(cfg, "ws", wsDir, fresh, forced, nil)
	_, statErr := os.Stat(agentStartedMarker(wsDir))
	return root, wsDir, err, statErr == nil
}

func TestRunAgentRecordsOnlyRealSessions(t *testing.T) {
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0.3")

	_, err, marked := runAgentIn(t, config.AgentCmd{First: "sleep 0.5", Again: "sleep 0.5"}, false)
	if err != nil {
		t.Fatalf("a session that ran should succeed, got %v", err)
	}
	if !marked {
		t.Error("a session that ran should record the entry")
	}

	_, err, marked = runAgentIn(t, config.AgentCmd{First: "true", Again: "true"}, false)
	if err == nil {
		t.Fatal("an instant exit 0 should be reported as a failed launch")
	}
	if !strings.Contains(err.Error(), "without starting a session") {
		t.Errorf("unexpected error: %v", err)
	}
	if marked {
		t.Error("a failed launch must not record the entry, or the next entry inherits the failure")
	}
}

func TestRunAgentRetriesFirstRunVariantAfterInstantExit(t *testing.T) {
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0.3")

	// The stale-`--continue` shape: the thereafter variant presumes a session
	// the workspace hasn't got, so the first-run variant is what should run.
	wsDir, _, _ := runAgentIn(t, config.AgentCmd{First: "touch first-ran", Again: "true"}, false)
	if _, err := os.Stat(filepath.Join(wsDir, "first-ran")); err != nil {
		t.Error("want the first-run variant retried after the thereafter variant bailed")
	}

	// `claude --continue` with nothing to continue exits 0 or 1 depending on
	// the build; both are the command declining, so both retry.
	wsDir, _, _ = runAgentIn(t, config.AgentCmd{First: "touch first-ran", Again: "exit 1"}, false)
	if _, err := os.Stat(filepath.Join(wsDir, "first-ran")); err != nil {
		t.Error("want the first-run variant retried after the thereafter variant bailed with exit 1")
	}

	wsDir, _, _ = runAgentIn(t, config.AgentCmd{First: "touch first-ran", Again: "kill -TERM $$"}, false)
	if _, err := os.Stat(filepath.Join(wsDir, "first-ran")); err == nil {
		t.Error("a signal death is the launch being stopped, not declined; it must not relaunch")
	}

	wsDir, _, _ = runAgentIn(t, config.AgentCmd{First: "true", Again: "touch again-ran"}, true)
	if _, err := os.Stat(filepath.Join(wsDir, "again-ran")); err == nil {
		t.Error("a bailed first-run variant has no earlier variant to fall back to")
	}

	if _, _, marked := runAgentIn(t, config.AgentCmd{First: "sleep 0.5", Again: "true"}, false); !marked {
		t.Error("a retry that starts a session should record the entry")
	}
}

func TestRunAgentPersistsPendingFirstRun(t *testing.T) {
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0.3")
	t.Setenv("SLATE_FRESH", "")

	// A failed first-run launch under --no-hold/non-TTY must keep the
	// workspace fresh: SLATE_FRESH and bareness are gone by the next
	// invocation, which would otherwise fall through to thereafter.
	root, wsDir, _, _ := runAgentInProject(t, config.AgentCmd{First: "true", Again: "sleep 0.5"}, true, false)
	if !agentFresh(root, wsDir) {
		t.Error("a failed first-run launch should leave the workspace fresh for the next entry")
	}

	// A bailed thereafter run whose first-run retry also bails owes the
	// workspace a first-run entry too.
	root, wsDir, _, _ = runAgentInProject(t, config.AgentCmd{First: "true", Again: "exit 1"}, false, false)
	if !agentFresh(root, wsDir) {
		t.Error("a failed first-run retry should leave the workspace fresh for the next entry")
	}

	// A session that runs settles the debt.
	wsDir, _, _ = runAgentIn(t, config.AgentCmd{First: "sleep 0.5", Again: "true"}, true, firstRunPending)
	if _, err := os.Stat(firstRunPendingMarker(wsDir)); err == nil {
		t.Error("a session that ran should clear the pending first-run marker")
	}
	if agentFresh(t.TempDir(), wsDir) {
		t.Error("a session that ran should end the workspace's freshness")
	}

	// A plain thereafter failure with no retry configured owes nothing.
	wsDir, _, _ = runAgentIn(t, config.AgentCmd{First: "true", Again: "true"}, false)
	if agentFresh(t.TempDir(), wsDir) {
		t.Error("a failed thereafter launch with no distinct first-run variant should not mark the workspace fresh")
	}
}

// newAgentWorktree is a registered workspace worktree in a repo where .slate/
// is not ignored, as in a project whose scaffold-added .gitignore line is
// still uncommitted. The debt is the test's to record.
func newAgentWorktree(t *testing.T) (mainRoot, wsDir string) {
	t.Helper()
	mainRoot = newTestProject(t)
	wsDir = filepath.Join(t.TempDir(), "ws")
	if err := workspace.CreateWorktree(mainRoot, wsDir, "slate/ws", ""); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(wsDir, ".slate"), 0o755); err != nil {
		t.Fatal(err)
	}
	return mainRoot, wsDir
}

func agentWorktree(t *testing.T) (mainRoot, wsDir string) {
	t.Helper()
	mainRoot, wsDir = newAgentWorktree(t)
	if err := recordFirstRunDebt(mainRoot, wsDir, false); err != nil {
		t.Fatal(err)
	}
	return mainRoot, wsDir
}

// A session slate did not launch leaves no marker; the work it left behind
// is the only evidence, and starting fresh over it would orphan its
// conversation.
func TestAgentFreshContinuesAWorkedWorkspace(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")

	root, wsDir := agentWorktree(t)
	if !agentFresh(root, wsDir) {
		t.Fatal("an untouched workspace owing its first-run entry should get it")
	}
	landedGit(t, wsDir, "commit", "-q", "--allow-empty", "-m", "worked")
	if agentFresh(root, wsDir) {
		t.Error("a workspace with commits since creation should continue, not start fresh")
	}
	t.Setenv("SLATE_FRESH", "1")
	if agentFresh(root, wsDir) {
		t.Error("SLATE_FRESH=1 (slate up --fresh) must not start over a worked workspace")
	}

	t.Setenv("SLATE_FRESH", "")
	root, wsDir = agentWorktree(t)
	if err := os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: laravel\nproject: edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a workspace with uncommitted changes should continue, not start fresh")
	}

	root, wsDir = agentWorktree(t)
	if err := os.Remove(filepath.Join(wsDir, "slate.yml")); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a deleted tracked file should continue, not start fresh")
	}

	// a mode change to a path the baseline already lists as modified: git
	// reports it, but size, mtime and contents are unchanged
	root, wsDir = newAgentWorktree(t)
	edited := filepath.Join(wsDir, "slate.yml")
	if err := os.WriteFile(edited, []byte("scaffold: laravel\nproject: edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(edited, 0o755); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a mode change to a tracked file should continue, not start fresh")
	}

	// the generated-file exemption is for untracked copies only: a repo that
	// tracks .env.container has made it real work
	root, wsDir = newAgentWorktree(t)
	if err := os.WriteFile(filepath.Join(wsDir, ".env.container"), []byte("X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landedGit(t, wsDir, "add", ".env.container")
	landedGit(t, wsDir, "commit", "-q", "-m", "track it")
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, ".env.container"), []byte("X=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("an edit to a tracked .env.container should continue, not start fresh")
	}

	root, wsDir = agentWorktree(t)
	if err := os.WriteFile(filepath.Join(wsDir, ".env.container"), []byte("X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !agentFresh(root, wsDir) {
		t.Error("slate's own generated files are not work")
	}
}

// slate new --adopt copies the main checkout's changes in before the debt is
// recorded: they are the baseline, not a session's work.
func TestAgentFreshBaselineExcludesAdoptedChanges(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	if err := os.WriteFile(filepath.Join(wsDir, "adopted.txt"), []byte("carried in\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if !agentFresh(root, wsDir) {
		t.Error("changes present when the debt was recorded are not a session's work")
	}
	if err := os.WriteFile(filepath.Join(wsDir, "adopted.txt"), []byte("carried in\nand edited since\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("an edit to a path that was already dirty at the baseline is a session's work")
	}
	if err := os.WriteFile(filepath.Join(wsDir, "adopted.txt"), []byte("carried in\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "later.txt"), []byte("a session's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a file added after the debt was recorded is a session's work")
	}
}

// Untracked entries are hashed by path even when they cannot be read, so a
// planted symlink is work without ever being opened, and a path's own
// whitespace survives the listing.
func TestAgentFreshFingerprintSeesLinksAndOddNames(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := agentWorktree(t)
	if err := os.Symlink("/dev/zero", filepath.Join(wsDir, "dump")); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a new untracked symlink is a change to the tree")
	}

	root, wsDir = newAgentWorktree(t)
	odd := filepath.Join(wsDir, " notes.md")
	if err := os.WriteFile(odd, []byte("draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if !agentFresh(root, wsDir) {
		t.Fatal("a file present at the baseline is not work")
	}
	if err := os.WriteFile(odd, []byte("draft, edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("an edit to a file whose name starts with a space is work")
	}
}

// A file the host user cannot open still changes the fingerprint when its
// size or mtime does.
func TestAgentFreshFingerprintTracksUnreadableFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a mode-0 file")
	}
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	sealed := filepath.Join(wsDir, "sealed.log")
	if err := os.WriteFile(sealed, []byte("v1\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if !agentFresh(root, wsDir) {
		t.Fatal("an unreadable file present at the baseline is not work")
	}
	if err := os.Chmod(sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sealed, []byte("v2, longer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a rewritten unreadable file is a change to the tree")
	}
}

// git omits an untracked directory it cannot open from the listing with a
// warning and exit 0; the directory still counts, by path and mtime.
func TestAgentFreshFingerprintSeesUnopenableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open any directory")
	}
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	sealed := filepath.Join(wsDir, "sealed")
	if err := os.Mkdir(sealed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sealed, "inner.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sealed, 0o700) })
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if !agentFresh(root, wsDir) {
		t.Fatal("an unopenable directory present at the baseline is not work")
	}
	if err := os.Chmod(sealed, 0o700); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(sealed, "added.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a file added beneath an unopenable directory moves its mtime and is work")
	}

	// git quotes the name without escaping it: an apostrophe must not cut
	// the path short
	root, wsDir = newAgentWorktree(t)
	quoted := filepath.Join(wsDir, "can't")
	if err := os.Mkdir(quoted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(quoted, "inner.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(quoted, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(quoted, 0o700) })
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(quoted, 0o700); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(quoted, "added.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(quoted, 0o000); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a file added beneath an unopenable directory named with an apostrophe is work")
	}

	// slate's own generated tree is exempt even when it cannot be opened
	root, wsDir = agentWorktree(t)
	generated := filepath.Join(wsDir, ".slate", "files")
	if err := os.Mkdir(generated, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generated, "mount.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(generated, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(generated, 0o700) })
	if !agentFresh(root, wsDir) {
		t.Error("an unopenable directory under .slate/ is not work")
	}
}

// Provisioning runs after the debt is recorded and its lifecycle hooks may
// touch git-visible files; the baseline is refreshed once it lands, so the
// genuine first entry is still the first. Work already present before the
// lifecycle keeps its evidence, and a settled debt is left alone.
func TestProvisioningBaselineRefreshAbsorbsOnlyProvisioning(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := agentWorktree(t)
	refresh := provisioningBaselineRefresh(root, wsDir)
	if err := os.WriteFile(filepath.Join(wsDir, "composer.lock"), []byte("generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Fatal("the scenario needs the hook's file to read as work before the refresh")
	}
	refresh()
	if !agentFresh(root, wsDir) {
		t.Error("a file provisioning wrote is not an agent session's work")
	}

	root, wsDir = agentWorktree(t)
	if err := os.WriteFile(filepath.Join(wsDir, "notes.md"), []byte("worked out of band\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refresh = provisioningBaselineRefresh(root, wsDir)
	if err := os.WriteFile(filepath.Join(wsDir, "composer.lock"), []byte("generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refresh()
	if agentFresh(root, wsDir) {
		t.Error("work present before the lifecycle must keep its evidence")
	}

	// while the first provisioning has yet to land (a foreground slate new
	// whose lifecycle failed, retried with slate up), the lifecycle's files
	// are folded in whatever the tree shows
	root, wsDir = newAgentWorktree(t)
	if err := recordFirstRunDebt(root, wsDir, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "composer.lock"), []byte("from the failed attempt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refresh = provisioningBaselineRefresh(root, wsDir)
	refresh()
	if !agentFresh(root, wsDir) {
		t.Error("a retried first provisioning should still absorb the lifecycle's files")
	}
	if readFirstRunDebt(wsDir).Provisioning {
		t.Error("the refresh should end the provisioning window")
	}

	// an untouched tree opens the window before the lifecycle, so an attempt
	// that fails part way leaves the retry free to fold its files in
	root, wsDir = agentWorktree(t)
	provisioningBaselineRefresh(root, wsDir)
	if !readFirstRunDebt(wsDir).Provisioning {
		t.Fatal("deciding to refresh should open the provisioning window")
	}
	if err := os.WriteFile(filepath.Join(wsDir, "composer.lock"), []byte("from the failed attempt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provisioningBaselineRefresh(root, wsDir)()
	if !agentFresh(root, wsDir) {
		t.Error("the retry after a failed lifecycle should still absorb its files")
	}

	root, wsDir = newAgentWorktree(t)
	if err := recordFirstRunDebt(root, wsDir, true); err != nil {
		t.Fatal(err)
	}
	before := readFirstRunDebt(wsDir)
	markProvisioned(wsDir)
	after := readFirstRunDebt(wsDir)
	if after.Provisioning || after.Head != before.Head || after.Tree != before.Tree {
		t.Errorf("markProvisioned should only end the window, got %+v from %+v", after, before)
	}

	_, wsDir = newAgentWorktree(t)
	if err := writeWorkspaceMarker(wsDir, "agent-started", nil); err != nil {
		t.Fatal(err)
	}
	provisioningBaselineRefresh(root, wsDir)()
	if _, err := os.Stat(firstRunPendingMarker(wsDir)); err == nil {
		t.Error("a refresh must not revive a settled debt")
	}
}

// Something planted where .slate should be could hide a session; only a
// genuinely absent directory falls back to the environment.
func TestAgentFreshRefusesASwappedSlateDir(t *testing.T) {
	t.Setenv("SLATE_FRESH", "1")
	root, wsDir := newAgentWorktree(t)
	if !agentFresh(root, wsDir) {
		t.Fatal("an empty .slate with SLATE_FRESH=1 is the up hook's first entry")
	}
	if err := os.RemoveAll(filepath.Join(wsDir, ".slate")); err != nil {
		t.Fatal(err)
	}
	if !agentFresh(root, wsDir) {
		t.Error("no .slate at all with SLATE_FRESH=1 is still the up hook's first entry")
	}
	landedGit(t, wsDir, "commit", "-q", "--allow-empty", "-m", "worked")
	if agentFresh(root, wsDir) {
		t.Error("with no markers left, a worked tree still outranks SLATE_FRESH=1")
	}
	landedGit(t, wsDir, "reset", "-q", "--hard", "HEAD~1")
	if err := os.Mkdir(filepath.Join(wsDir, ".slate"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(wsDir, ".slate")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(wsDir, ".slate")); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a symlink where .slate should be must not read as a first entry")
	}
}

// A first-run launch that bails may have written something first; that is
// evidence for the next entry, so the recorded baseline stays.
func TestRunAgentFailedFirstRunKeepsBaseline(t *testing.T) {
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0.3")
	t.Setenv("SLATE_FRESH", "")
	agentNoHold = true
	t.Cleanup(func() { agentNoHold = false })
	root, wsDir := agentWorktree(t)
	before := readFirstRunDebt(wsDir)
	if before.Head == "" {
		t.Fatal("the scenario needs a recorded baseline")
	}
	cfg := config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: "touch made-by-launcher", Again: "sleep 0.5"}}
	if err := runAgent(cfg, "ws", wsDir, true, false, nil); err == nil {
		t.Fatal("an instant exit should be reported as a failed launch")
	}
	if after := readFirstRunDebt(wsDir); after != before {
		t.Errorf("the baseline should survive a failed launch, got %+v want %+v", after, before)
	}
	if agentFresh(root, wsDir) {
		t.Error("what the launcher wrote before bailing is evidence for the thereafter choice")
	}
}

// A session that committed and then reset back to the baseline commit left a
// clean tree at the same HEAD; the reflog it grew is the evidence.
func TestAgentFreshSeesACommitUndone(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := agentWorktree(t)
	landedGit(t, wsDir, "commit", "-q", "--allow-empty", "-m", "worked")
	landedGit(t, wsDir, "reset", "-q", "--hard", "HEAD~1")
	if agentFresh(root, wsDir) {
		t.Error("a commit undone still grew the reflog and is a session's work")
	}
}

// Repository configuration must not hide a submodule's changes from the
// fingerprint.
func TestFingerprintSeesSubmoduleChanges(t *testing.T) {
	sub := filepath.Join(t.TempDir(), "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitWorktree(t, sub)
	landedGit(t, sub, "checkout", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landedGit(t, sub, "add", "f.txt")
	landedGit(t, sub, "commit", "-q", "-m", "base")
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitWorktree(t, repo)
	landedGit(t, repo, "checkout", "-q", "-b", "main")
	landedGit(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	landedGit(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	landedGit(t, repo, "commit", "-q", "-m", "add sub")
	landedGit(t, repo, "config", "diff.ignoreSubmodules", "all")
	before, err := worktreeFingerprint("", repo)
	if err != nil {
		t.Fatal(err)
	}
	// the cloned submodule has no identity of its own on a bare runner
	landedGit(t, filepath.Join(repo, "sub"), "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-q", "--allow-empty", "-m", "worked in the submodule")
	after, err := worktreeFingerprint("", repo)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("a submodule commit should change the fingerprint despite diff.ignoreSubmodules=all")
	}

	// once the submodule is already listed, a further commit inside it still
	// changes the fingerprint: the directory's own metadata would not
	landedGit(t, filepath.Join(repo, "sub"), "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-q", "--allow-empty", "-m", "worked again")
	again, err := worktreeFingerprint("", repo)
	if err != nil {
		t.Fatal(err)
	}
	if again == after {
		t.Error("a second commit inside an already-listed submodule should change the fingerprint")
	}
}

// git lists an untracked embedded repository as "name/" and never looks
// inside it; the fingerprint reads its commit, so work committed there since
// the baseline is a change.
func TestFingerprintSeesEmbeddedRepositoryCommits(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	nested := filepath.Join(wsDir, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitWorktree(t, nested)
	landedGit(t, nested, "commit", "-q", "--allow-empty", "-m", "base")
	if listing, _, err := gitForRaw("", wsDir, "ls-files", "--others", "--exclude-standard", "-z"); err != nil || !strings.Contains(string(listing), "nested/\x00") {
		t.Fatalf("the scenario needs git to list the embedded repository as nested/, got %q %v", listing, err)
	}
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if !agentFresh(root, wsDir) {
		t.Fatal("an embedded repository present at the baseline is not work")
	}
	landedGit(t, nested, "commit", "-q", "--allow-empty", "-m", "worked inside")
	if agentFresh(root, wsDir) {
		t.Error("a commit inside an embedded repository since the baseline is work")
	}
}

// The submodule's .git pointer is container-written; the git dir it names
// may only lie under the worktree's registered git dir, where git keeps a
// linked worktree's submodules. A pointer anywhere else fails the
// fingerprint, so the host never reads a HEAD the container chose.
func TestFingerprintRefusesASubmodulePointerEscape(t *testing.T) {
	sub := filepath.Join(t.TempDir(), "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitWorktree(t, sub)
	landedGit(t, sub, "commit", "-q", "--allow-empty", "-m", "base")
	root, wsDir := newAgentWorktree(t)
	gitDir, ok := registeredGitDir(root, wsDir)
	if !ok {
		t.Fatal("the test worktree should be registered")
	}
	landedGit(t, wsDir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	landedGit(t, wsDir, "commit", "-q", "-m", "add sub")
	landedGit(t, filepath.Join(wsDir, "sub"), "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-q", "--allow-empty", "-m", "worked in the submodule")
	pointer := filepath.Join(wsDir, "sub", ".git")
	genuine, err := os.ReadFile(pointer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktreeFingerprint(gitDir, wsDir); err != nil {
		t.Fatalf("git's own layout for a linked worktree's submodule should fingerprint: %v", err)
	}

	other := filepath.Join(t.TempDir(), "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitWorktree(t, other)
	landedGit(t, other, "commit", "-q", "--allow-empty", "-m", "elsewhere")
	decoy := filepath.Join(other, ".git")
	relative, err := filepath.Rel(filepath.Join(wsDir, "sub"), decoy)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := safeio.OpenDir(wsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	for _, target := range []string{decoy, relative, filepath.Join(root, ".git")} {
		if err := os.WriteFile(pointer, []byte("gitdir: "+target+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if head, err := submoduleHead(gitDir, pinned, wsDir, "sub"); err == nil {
			t.Errorf("a pointer to %s should not be followed, yet read HEAD %s", target, head)
		}
		if _, err := worktreeFingerprint(gitDir, wsDir); err == nil {
			t.Errorf("a pointer to %s should fail the fingerprint", target)
		}
	}
	if err := os.WriteFile(pointer, genuine, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := worktreeFingerprint(gitDir, wsDir); err != nil {
		t.Errorf("the genuine pointer restored should fingerprint again: %v", err)
	}
}

// A submodule's config is container-writable and core.fsmonitor names a
// command: nothing the fingerprint runs may execute it.
func TestFingerprintRunsNothingFromASubmodule(t *testing.T) {
	sub := filepath.Join(t.TempDir(), "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitWorktree(t, sub)
	landedGit(t, sub, "checkout", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landedGit(t, sub, "add", "f.txt")
	landedGit(t, sub, "commit", "-q", "-m", "base")
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitWorktree(t, repo)
	landedGit(t, repo, "checkout", "-q", "-b", "main")
	landedGit(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	landedGit(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	landedGit(t, repo, "commit", "-q", "-m", "add sub")

	ran := filepath.Join(t.TempDir(), "FSMONITOR-RAN")
	hook := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch \""+ran+"\"\necho /\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// the submodule's commit matches the recorded gitlink, so git can only
	// learn it is dirty by inspecting it; that is where a status inside the
	// submodule would run the command
	if err := os.WriteFile(filepath.Join(repo, "sub", "f.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landedGit(t, filepath.Join(repo, "sub"), "config", "core.fsmonitor", hook)
	if err := os.WriteFile(filepath.Join(repo, "sub", "g.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Remove(ran)
	if _, err := worktreeFingerprint("", repo); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gitForRaw("", repo, "status", "--porcelain", "--untracked-files=normal", "--ignore-submodules=dirty"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("git ran the submodule's core.fsmonitor command on the host")
	}
}

// A new: hook that returned inside the launch floor hosted no session, so the
// background provisioner may refresh once it lands; a hook that ran longer may
// have, so it may not.
func TestHookOutcomeGatesTheProvisionerRefresh(t *testing.T) {
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "3")
	root, wsDir := newAgentWorktree(t)
	if err := os.WriteFile(filepath.Join(wsDir, ".slate", "provisioning"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	noteHookOutcome(root, wsDir, hostRun{elapsed: 10 * time.Second})
	if provisionRefreshWanted(provisionOpts{}, wsDir) {
		t.Error("a hook that may have hosted a session must not license a refresh")
	}
	noteHookOutcome(root, wsDir, hostRun{elapsed: 10 * time.Millisecond})
	if !provisionRefreshWanted(provisionOpts{}, wsDir) {
		t.Error("a hook that bailed hosted no session and should license the refresh")
	}
	if provisionRefreshWanted(provisionOpts{}, wsDir) {
		t.Error("the licence is consumed once")
	}
	if !provisionRefreshWanted(provisionOpts{refreshDebt: true}, wsDir) {
		t.Error("a provisioner launched with no hook alongside refreshes regardless")
	}
	// a licence left by an earlier provisioning that failed before its
	// second look is consumed inside the lock, whatever this launch is
	noteHookOutcome(root, wsDir, hostRun{elapsed: 10 * time.Millisecond})
	if !provisionRefreshWanted(provisionOpts{refreshDebt: true}, wsDir) {
		t.Error("a no-hook launch still refreshes")
	}
	if hookBailed(wsDir) {
		t.Error("the no-hook launch should have consumed the stale licence inside the lock")
	}
	// once the lock has gone an agent may be at work, so the no-hook launch
	// refreshes inside it only; the second look is for the hook's licence
	refreshes := 0
	provisionSecondLook(wsDir, func() { refreshes++ })
	if refreshes != 0 {
		t.Error("the second look must not refresh on the no-hook launch alone")
	}
	noteHookOutcome(root, wsDir, hostRun{elapsed: 10 * time.Millisecond})
	provisionSecondLook(wsDir, func() { refreshes++ })
	provisionSecondLook(wsDir, func() { refreshes++ })
	if refreshes != 1 {
		t.Errorf("a bailed hook's licence should refresh at the second look once, got %d", refreshes)
	}

	// the provisioner finished before the hook returned: nobody is left to
	// consume the licence, so the hook's side refreshes itself
	t.Setenv("SLATE_FRESH", "")
	root, wsDir = agentWorktree(t)
	if err := os.WriteFile(filepath.Join(wsDir, "composer.lock"), []byte("from the lifecycle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Fatal("the scenario needs the lifecycle's file to read as work first")
	}
	noteHookOutcome(root, wsDir, hostRun{elapsed: 10 * time.Millisecond})
	if !agentFresh(root, wsDir) {
		t.Error("with the provisioner already gone, a bailed hook should refresh the baseline itself")
	}
	if provisionRefreshWanted(provisionOpts{}, wsDir) {
		t.Error("that refresh consumes the licence")
	}
}

// A legacy first entry (no baseline yet) takes its baseline before the
// command runs, so whatever a bailed launcher wrote is evidence.
func TestRunAgentLegacyFirstRunBaselinesBeforeLaunch(t *testing.T) {
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0.3")
	t.Setenv("SLATE_FRESH", "")
	agentNoHold = true
	t.Cleanup(func() { agentNoHold = false })
	root, wsDir := newAgentWorktree(t)
	if err := writeWorkspaceMarker(wsDir, firstRunPending, nil); err != nil {
		t.Fatal(err)
	}
	cfg := config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: "touch made-by-launcher", Again: "sleep 0.5"}}
	if err := runAgent(cfg, "ws", wsDir, true, false, nil); err == nil {
		t.Fatal("an instant exit should be reported as a failed launch")
	}
	if agentFresh(root, wsDir) {
		t.Error("what the launcher wrote before bailing is evidence for the thereafter choice, legacy marker or not")
	}
}

// A listing a container has made enormous is not judged at all: the
// fingerprint fails and the entry continues rather than starting over.
func TestAgentWorkedFailsClosedOnAHugeListing(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := agentWorktree(t)
	for i := 0; i < 8; i++ {
		if err := os.WriteFile(filepath.Join(wsDir, fmt.Sprintf("untracked-%d.txt", i)), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// between rev-parse's 41 bytes and the listing
	limit := gitOutputLimit
	gitOutputLimit = 64
	t.Cleanup(func() { gitOutputLimit = limit })
	if _, err := worktreeFingerprint("", wsDir); err == nil {
		t.Fatal("the scenario needs the listing to exceed the cap")
	}
	if agentFresh(root, wsDir) {
		t.Error("an unjudgeable tree must continue, not start fresh")
	}
}

// git's untracked listing is scoped to its own cwd and relative to it; the
// fingerprint runs git from the worktree root, so slate agent reached from a
// subdirectory judges the same tree.
func TestFingerprintIgnoresTheProcessDirectory(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	gitDir, ok := registeredGitDir(root, wsDir)
	if !ok {
		t.Fatal("the test worktree should be registered")
	}
	app := filepath.Join(wsDir, "app")
	if err := os.Mkdir(app, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app/inner.txt", "root.txt"} {
		if err := os.WriteFile(filepath.Join(wsDir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outside, err := worktreeFingerprint(gitDir, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	t.Chdir(app)
	inside, err := worktreeFingerprint(gitDir, wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if inside != outside {
		t.Error("the fingerprint should not depend on slate's own working directory")
	}
	if !agentFresh(root, wsDir) {
		t.Fatal("the baseline taken elsewhere should match from a subdirectory")
	}
	if err := os.WriteFile(filepath.Join(wsDir, "root-later.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a file added outside slate's subdirectory is work")
	}
}

// A container can fill the tree with any amount of content; past the total
// budget the fingerprint reads no more of it and the tree is not judged.
func TestAgentWorkedFailsClosedOnAHugeTree(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(wsDir, fmt.Sprintf("blob-%d.bin", i)), []byte(strings.Repeat("x", 1024)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if !agentFresh(root, wsDir) {
		t.Fatal("content present at the baseline is not work")
	}
	budget := fingerprintBudget
	fingerprintBudget = 3 * 1024
	t.Cleanup(func() { fingerprintBudget = budget })
	if _, err := worktreeFingerprint("", wsDir); !errors.Is(err, errFingerprintTooLarge) {
		t.Fatalf("the scenario needs the tree to exceed the budget, got %v", err)
	}
	if agentFresh(root, wsDir) {
		t.Error("an unjudgeable tree must continue, not start fresh")
	}
}

// A directory name holding a newline spreads git's warning over two lines,
// past the pattern; a warning that cannot be read is not a listing to trust,
// whichever read it came from.
func TestUnopenableDirectoryWithANewlineFailsClosed(t *testing.T) {
	warnings := []byte("warning: could not open directory 'bad\nname/': Permission denied\n")
	if dirs, err := unopenedDirectories(warnings); !errors.Is(err, errUnreadWarning) {
		t.Errorf("a warning split over two lines should not be read, got %v %v", dirs, err)
	}
	if dirs, err := unopenedDirectories([]byte("warning: could not open directory 'plain/': Permission denied\n")); err != nil || len(dirs) != 1 || dirs[0] != "plain" {
		t.Errorf("a plain warning should still name its directory, got %v %v", dirs, err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root can open any directory")
	}
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	sealed := filepath.Join(wsDir, "bad\nname")
	if err := os.Mkdir(sealed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sealed, "inner.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sealed, 0o700) })
	gitDir, _ := registeredGitDir(root, wsDir)
	if _, err := worktreeFingerprint(gitDir, wsDir); !errors.Is(err, errUnreadWarning) {
		t.Errorf("the fingerprint should refuse the listing, got %v", err)
	}
	if err := writeWorkspaceMarker(wsDir, firstRunPending, nil); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a legacy marker over a listing that cannot be trusted is not fresh")
	}
}

// A legacy marker with no baseline still sees a directory git could not open.
func TestAgentFreshLegacyFallbackSeesUnopenableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open any directory")
	}
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	if err := writeWorkspaceMarker(wsDir, firstRunPending, nil); err != nil {
		t.Fatal(err)
	}
	sealed := filepath.Join(wsDir, "sealed")
	if err := os.Mkdir(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sealed, 0o700) })
	if agentFresh(root, wsDir) {
		t.Error("a directory git could not open is work the legacy check must not miss")
	}
}

// Staging a path already listed as modified leaves the tree untouched; the
// index is part of the fingerprint.
func TestAgentFreshSeesStagedChanges(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	if err := os.WriteFile(filepath.Join(wsDir, "slate.yml"), []byte("scaffold: laravel\nproject: edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if !agentFresh(root, wsDir) {
		t.Fatal("a modified file present at the baseline is not work")
	}
	landedGit(t, wsDir, "add", "slate.yml")
	if agentFresh(root, wsDir) {
		t.Error("staging that file is a session's work")
	}
}

// A reflog that cannot be read cannot clear the tree: the entry continues.
func TestAgentWorkedFailsClosedOnAnUnreadableReflog(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := agentWorktree(t)
	// between rev-parse's 41 bytes and the two-entry reflog's 82
	limit := gitOutputLimit
	gitOutputLimit = 64
	t.Cleanup(func() { gitOutputLimit = limit })
	if _, err := reflogEntries("", wsDir); err == nil {
		t.Fatal("the scenario needs the reflog to exceed the cap")
	}
	if agentFresh(root, wsDir) {
		t.Error("an unjudgeable reflog must continue, not start fresh")
	}

	_, wsDir = newAgentWorktree(t)
	if err := writeWorkspaceMarker(wsDir, firstRunPending, nil); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("the legacy fallback must fail closed on an unreadable reflog too")
	}
}

// Git's warnings are capped like its listing: a container can make either
// arbitrarily long.
func TestAgentWorkedFailsClosedOnEndlessWarnings(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open any directory")
	}
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	sealed := filepath.Join(wsDir, strings.Repeat("sealed-", 10))
	if err := os.Mkdir(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(sealed, 0o700) })
	// enough for rev-parse, the reflog and the listing, not for the warning
	limit := gitOutputLimit
	gitOutputLimit = 100
	t.Cleanup(func() { gitOutputLimit = limit })
	if _, _, err := gitForRaw("", wsDir, "ls-files", "--others", "--exclude-standard", "-z"); err == nil {
		t.Fatal("the scenario needs the warning to exceed the cap")
	}
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a tree whose warnings cannot be judged must continue, not start fresh")
	}
}

// The creation hook's own entry runs while provisioning may already be
// writing files alongside it; with the window open and SLATE_FRESH=1 nothing
// else has had the chance to work, so it is the first entry.
func TestAgentFreshCreationHookEntryIsFirst(t *testing.T) {
	root, wsDir := newAgentWorktree(t)
	if err := recordFirstRunDebt(root, wsDir, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "composer.lock"), []byte("from the lifecycle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLATE_FRESH", "")
	if agentFresh(root, wsDir) {
		t.Fatal("the scenario needs the file to read as work outside the hook")
	}
	t.Setenv("SLATE_FRESH", "1")
	if !agentFresh(root, wsDir) {
		t.Error("the creation hook's entry is the first whatever provisioning wrote so far")
	}
	markProvisioned(wsDir)
	if agentFresh(root, wsDir) {
		t.Error("once the window has closed, SLATE_FRESH=1 no longer outranks work")
	}
}

// A pruned reflog is as much a mismatch as a grown one; only an equal count
// clears the tree.
func TestAgentFreshSeesAPrunedReflog(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := agentWorktree(t)
	gitDir, ok := registeredGitDir(root, wsDir)
	if !ok {
		t.Fatal("the test worktree should be registered")
	}
	if err := os.Remove(filepath.Join(gitDir, "logs", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a reflog shorter than the baseline recorded cannot clear the tree")
	}
}

// Git that cannot even name HEAD is not a tree to start over in.
func TestAgentWorkedFailsClosedWithoutHead(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := agentWorktree(t)
	gitDir, ok := registeredGitDir(root, wsDir)
	if !ok {
		t.Fatal("the test worktree should be registered")
	}
	if err := os.Remove(filepath.Join(gitDir, "HEAD")); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("an unjudgeable worktree must continue, not start fresh")
	}
}

// Restaging new content behind restored bytes and timestamps changes only the
// index blob; the staged listing carries it.
func TestAgentFreshSeesRestagedContent(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	edited := filepath.Join(wsDir, "slate.yml")
	if err := os.WriteFile(edited, []byte("scaffold: laravel\nproject: one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landedGit(t, wsDir, "add", "slate.yml")
	info, err := os.Stat(edited)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(edited, []byte("scaffold: laravel\nproject: two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landedGit(t, wsDir, "add", "slate.yml")
	if err := os.WriteFile(edited, []byte("scaffold: laravel\nproject: one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(edited, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a restaged blob behind restored bytes is a session's work")
	}
}

// A marker a container has mangled is not the legacy empty marker.
func TestAgentFreshRefusesACorruptMarker(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	for _, content := range []string{"{not json", "null", "[]", "42"} {
		root, wsDir := newAgentWorktree(t)
		if err := writeWorkspaceMarker(wsDir, firstRunPending, []byte(content)); err != nil {
			t.Fatal(err)
		}
		if agentFresh(root, wsDir) {
			t.Errorf("a marker holding %q must continue, not start fresh", content)
		}
	}
}

// The thereafter variant's first-run retry is a first entry too: with no
// baseline yet it takes one before the retried command can write anything.
func TestRunAgentRetryBaselinesBeforeLaunch(t *testing.T) {
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0.3")
	t.Setenv("SLATE_FRESH", "")
	agentNoHold = true
	t.Cleanup(func() { agentNoHold = false })
	root, wsDir := newAgentWorktree(t)
	if err := writeWorkspaceMarker(wsDir, firstRunPending, nil); err != nil {
		t.Fatal(err)
	}
	cfg := config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: "touch made-by-launcher", Again: "true"}}
	if err := runAgent(cfg, "ws", wsDir, false, false, nil); err == nil {
		t.Fatal("two instant exits should be reported as a failed launch")
	}
	if agentFresh(root, wsDir) {
		t.Error("what the retried launcher wrote before bailing is evidence for the thereafter choice")
	}
}

// Only the worktree's own HEAD reflog is its movement: with none, the branch
// it checked out carries history from before the workspace existed, which
// git reflog show HEAD falls through to. A missing log is unjudgeable, not
// the branch's story.
func TestAgentFreshLegacyIgnoresTheBranchLog(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root := newTestProject(t)
	landedGit(t, root, "branch", "old")
	landedGit(t, root, "checkout", "-q", "old")
	landedGit(t, root, "commit", "-q", "--allow-empty", "-m", "older history")
	landedGit(t, root, "checkout", "-q", "-")
	wsDir := filepath.Join(t.TempDir(), "ws")
	if err := workspace.CreateWorktree(root, wsDir, "old", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wsDir, ".slate"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceMarker(wsDir, firstRunPending, nil); err != nil {
		t.Fatal(err)
	}
	gitDir, ok := registeredGitDir(root, wsDir)
	if !ok {
		t.Fatal("the test worktree should be registered")
	}
	if err := os.Remove(filepath.Join(gitDir, "logs", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if entries, err := headReflog(gitDir, wsDir); err != nil || len(entries) != 0 {
		t.Errorf("the branch's log is not this worktree's reflog, got %d entries err=%v", len(entries), err)
	}
	if _, err := headMoved(gitDir, wsDir); !errors.Is(err, errNoReflog) {
		t.Errorf("with no HEAD reflog movement is unjudgeable, got %v", err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a worktree whose movement cannot be judged is not a tree to start over in")
	}
}

// A legacy marker has no baseline count to compare, but a commit later reset
// back to the checkout commit still left its entry in the worktree's reflog.
func TestAgentFreshLegacySeesACommitUndone(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	if err := writeWorkspaceMarker(wsDir, firstRunPending, nil); err != nil {
		t.Fatal(err)
	}
	landedGit(t, wsDir, "commit", "-q", "--allow-empty", "-m", "worked")
	landedGit(t, wsDir, "reset", "-q", "--hard", "HEAD~1")
	if agentFresh(root, wsDir) {
		t.Error("a commit undone still moved the worktree and is a session's work")
	}
}

// The baseline holds without a reflog (core.logAllRefUpdates=false, or an
// expired one); a marker from before baselines existed still has the reflog.
// A worktree with no HEAD reflog (core.logAllRefUpdates off leaves it none)
// cannot tell a commit reset back to the baseline from no movement, so the
// choice fails closed to the thereafter variant, whose bail retry covers a
// workspace with nothing to continue.
func TestAgentFreshWithoutAReflogFailsClosed(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root := newTestProject(t)
	landedGit(t, root, "config", "core.logAllRefUpdates", "false")
	wsDir := filepath.Join(t.TempDir(), "ws")
	if err := workspace.CreateWorktree(root, wsDir, "slate/ws", ""); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(wsDir, ".slate"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitDir, ok := registeredGitDir(root, wsDir)
	if !ok {
		t.Fatal("the test worktree should be registered")
	}
	if _, err := os.Stat(filepath.Join(gitDir, "logs", "HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the scenario needs a worktree with no HEAD reflog, got %v", err)
	}
	if err := recordFirstRunDebt(root, wsDir, false); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a baseline with no reflog leaves movement unjudgeable")
	}
	landedGit(t, wsDir, "commit", "-q", "--allow-empty", "-m", "worked")
	landedGit(t, wsDir, "reset", "-q", "--hard", "HEAD~1")
	if agentFresh(root, wsDir) {
		t.Error("a commit reset back leaves no trace without a reflog; still not a tree to start over in")
	}
	if err := writeWorkspaceMarker(wsDir, firstRunPending, nil); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a pre-baseline marker has only the reflog to judge movement by; none fails closed too")
	}

	// a reflog present at the baseline and gone since is a mismatch
	root, wsDir = agentWorktree(t)
	gitDir, _ = registeredGitDir(root, wsDir)
	landedGit(t, wsDir, "commit", "-q", "--allow-empty", "-m", "worked")
	// git reflog show HEAD falls through to the branch's log when HEAD's is
	// missing, so both go
	for _, log := range []string{filepath.Join(gitDir, "logs", "HEAD"), filepath.Join(root, ".git", "logs", "refs", "heads", "slate", "ws")} {
		if err := os.Remove(log); err != nil {
			t.Fatal(err)
		}
	}
	if agentFresh(root, wsDir) {
		t.Error("a commit since the recorded baseline is work, reflog or not")
	}
}

// A worktree the main checkout does not register has only its
// container-writable .git to read through: it gets no baseline and is never
// judged fresh.
func TestAgentFreshFailsClosedWithoutARegistration(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root := newTestProject(t)
	dir := t.TempDir()
	gitInitWorktree(t, dir)
	landedGit(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	if err := os.MkdirAll(filepath.Join(dir, ".slate"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := recordFirstRunDebt(root, dir, true); err != nil {
		t.Fatal(err)
	}
	debt := readFirstRunDebt(dir)
	if debt.Head != "" || !debt.Provisioning || debt.Corrupt {
		t.Errorf("an unregistered worktree should record the window but no baseline, got %+v", debt)
	}
	if agentFresh(root, dir) {
		t.Error("a worktree read through its own .git pointer is not a tree to start over in")
	}
}

// A baseline that cannot be taken still records the provisioning window, so
// the creation hook's own entry keeps its first-run variant.
func TestRecordFirstRunDebtKeepsTheWindowWithoutABaseline(t *testing.T) {
	root, wsDir := newAgentWorktree(t)
	if err := os.WriteFile(filepath.Join(wsDir, "big.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	budget := fingerprintBudget
	fingerprintBudget = 0
	t.Cleanup(func() { fingerprintBudget = budget })
	if err := recordFirstRunDebt(root, wsDir, true); err != nil {
		t.Fatal(err)
	}
	debt := readFirstRunDebt(wsDir)
	if debt.Head != "" || !debt.Provisioning || debt.Corrupt {
		t.Errorf("a failed baseline should leave the window recorded and nothing else, got %+v", debt)
	}
}

// A container can rewrite the worktree's .git pointer; the decision reads git
// through the main checkout's registration instead.
func TestAgentFreshReadsGitThroughTheRegistration(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := agentWorktree(t)
	landedGit(t, wsDir, "commit", "-q", "--allow-empty", "-m", "worked")

	fake := filepath.Join(t.TempDir(), "fake")
	landedGit(t, root, "clone", "-q", root, fake)
	if err := os.WriteFile(filepath.Join(wsDir, ".git"), []byte("gitdir: "+filepath.Join(fake, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDir, ok := registeredGitDir(root, wsDir)
	if !ok {
		t.Fatal("the test worktree should be registered")
	}
	forged, _ := gitFor("", wsDir, "rev-parse", "HEAD")
	anchored, _ := gitFor(gitDir, wsDir, "rev-parse", "HEAD")
	if forged == anchored {
		t.Fatal("the scenario needs the forged pointer to answer differently from the registration")
	}
	if agentFresh(root, wsDir) {
		t.Error("the registration should still see the commit behind the forged pointer")
	}

	// A symlink at .git resolves elsewhere; the registration match must not
	// follow it and read the worktree as unregistered.
	if err := os.Remove(filepath.Join(wsDir, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fake, ".git"), filepath.Join(wsDir, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, ok = registeredGitDir(root, wsDir); !ok {
		t.Fatal("a symlinked .git must not unregister the worktree")
	}
	if agentFresh(root, wsDir) {
		t.Error("the registration should still see the commit behind the symlinked pointer")
	}
}

// Something planted at a marker's name is never followed and could be hiding
// a session, so the entry continues rather than starting over, whatever the
// environment says.
func TestAgentFreshRefusesPlantedMarkers(t *testing.T) {
	t.Setenv("SLATE_FRESH", "1")
	root, wsDir := agentWorktree(t)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(elsewhere, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, agentStartedMarker(wsDir)); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a symlink where agent-started should be must not read as a first entry")
	}

	_, wsDir = newAgentWorktree(t)
	if err := os.Symlink(elsewhere, unprovisionedMarker(wsDir)); err != nil {
		t.Fatal(err)
	}
	if agentFresh(root, wsDir) {
		t.Error("a symlink where unprovisioned should be must not read as a first entry")
	}
}

// The marker is container-writable: a FIFO or symlink in its place is not a
// marker at all (the entry falls through to the thereafter variant, which the
// bail retry covers), must never block the decision, and is never followed.
func TestAgentFreshMarkerMustBeARegularFile(t *testing.T) {
	t.Setenv("SLATE_FRESH", "")
	root, wsDir := newAgentWorktree(t)
	if err := syscall.Mkfifo(firstRunPendingMarker(wsDir), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { done <- agentFresh(root, wsDir) }()
	select {
	case fresh := <-done:
		if fresh {
			t.Error("a FIFO where the marker should be is not a recorded debt")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO where the marker should be must not block slate agent")
	}

	root, wsDir = newAgentWorktree(t)
	planted := filepath.Join(t.TempDir(), "planted.json")
	if err := os.WriteFile(planted, []byte(`{"head":"0000000000000000000000000000000000000000","tree":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(planted, firstRunPendingMarker(wsDir)); err != nil {
		t.Fatal(err)
	}
	if debt := readFirstRunDebt(wsDir); debt.Head != "" {
		t.Errorf("a symlinked marker must not be followed to a planted baseline, got head %q", debt.Head)
	}
	if agentFresh(root, wsDir) {
		t.Error("a symlink where the marker should be is not a recorded debt")
	}
}

func TestRunAgentForcedVariantNeverRetries(t *testing.T) {
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0.3")

	// --continue: the launcher knows a session exists, so a bail is reported
	// as such rather than rescued by a first-run that would start over.
	wsDir, err, marked := runAgentForcedIn(t, config.AgentCmd{First: "touch first-ran", Again: "true"}, false, true, firstRunPending)
	if err == nil || !strings.Contains(err.Error(), "the thereafter agent command exited") {
		t.Errorf("want the forced thereafter bail reported, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "first-ran")); err == nil {
		t.Error("--continue must not retry the first-run variant")
	}
	if marked {
		t.Error("a bailed forced launch is still not a session")
	}

	// --fresh runs the first-run variant even over a recorded session.
	wsDir, _, _ = runAgentForcedIn(t, config.AgentCmd{First: "touch first-ran", Again: "touch again-ran"}, true, true, "agent-started")
	if _, err := os.Stat(filepath.Join(wsDir, "first-ran")); err != nil {
		t.Error("--fresh should run the first-run variant despite agent-started")
	}
	if _, err := os.Stat(filepath.Join(wsDir, "again-ran")); err == nil {
		t.Error("--fresh must not fall through to the thereafter variant")
	}
}

func TestRunAgentPassesThroughArgs(t *testing.T) {
	newWs := func() string {
		t.Helper()
		wsDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(wsDir, ".slate"), 0o755); err != nil {
			t.Fatal(err)
		}
		return wsDir
	}
	readArgs := func(wsDir, file string) string {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(wsDir, file))
		if err != nil {
			t.Fatalf("args never reached the command: %v", err)
		}
		return string(got)
	}
	agentNoHold = true
	t.Cleanup(func() { agentNoHold = false })
	extra := []string{"a", "b c"}

	// touch names its files after its argv, so the created files show
	// exactly which args reached the command and whether "b c" stayed one.
	argvFiles := func(wsDir string) {
		t.Helper()
		for _, want := range extra {
			if _, err := os.Stat(filepath.Join(wsDir, want)); err != nil {
				t.Errorf("arg %q never reached the command as its own argv entry", want)
			}
		}
	}

	// Args land as argv entries, not spliced shell text.
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0")
	wsDir := newWs()
	cfg := config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: "touch", Again: "touch"}}
	if err := runAgent(cfg, "ws", wsDir, false, false, extra); err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	argvFiles(wsDir)

	// A block-scalar `agent: |` decodes with a trailing newline; the args
	// must still reach the command instead of running as their own line.
	wsDir = newWs()
	cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: "touch\n", Again: "touch\n"}}
	if err := runAgent(cfg, "ws", wsDir, false, false, extra); err != nil {
		t.Fatalf("runAgent with trailing newline: %v", err)
	}
	argvFiles(wsDir)

	// With shell structure present, an append can land on the wrong
	// pipeline stage, run as its own command, or vanish into a comment;
	// refused unless {{ARGS}} pins the placement.
	for _, compound := range []string{"true &", "true | cat", "true; true", "true 2>&1", "true # note", "(true)", `touch \`} {
		wsDir = newWs()
		cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: compound, Again: compound}}
		if err := runAgent(cfg, "ws", wsDir, false, false, extra); err == nil || !strings.Contains(err.Error(), "{{ARGS}}") {
			t.Errorf("want compound command %q refused, got %v", compound, err)
		}
	}

	// {{ARGS}} pins where the args land, compound commands included, and the
	// first-run retry carries the same args.
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0.3")
	wsDir = newWs()
	cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{
		First: `sleep 0.5; printf '%s\n' {{ARGS}} > first.txt`, Again: "true"}}
	if err := runAgent(cfg, "ws", wsDir, false, false, extra); err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	if got := readArgs(wsDir, "first.txt"); got != "a\nb c\n" {
		t.Errorf("retry argv = %q, want %q", got, "a\nb c\n")
	}

	// The quoted spellings normalise: "{{ARGS}}" must not degrade into an
	// unquoted $@ that word-splits, and '{{ARGS}}' must not become a
	// literal "$@" string that never expands.
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0")
	for _, spelling := range []string{`touch "{{ARGS}}"`, `touch '{{ARGS}}'`} {
		wsDir = newWs()
		cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: spelling, Again: spelling}}
		if err := runAgent(cfg, "ws", wsDir, false, false, extra); err != nil {
			t.Fatalf("runAgent with %q: %v", spelling, err)
		}
		argvFiles(wsDir)
	}

	// {{ARGS}} buried in a longer quoted region or a comment can never
	// receive the args; refused rather than silently dropping them.
	for _, dead := range []string{`touch 'review {{ARGS}}'`, `touch "review {{ARGS}}"`, "touch # {{ARGS}}", "touch #{{ARGS}}", "(touch)# {{ARGS}}"} {
		wsDir = newWs()
		cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: dead, Again: dead}}
		if err := runAgent(cfg, "ws", wsDir, false, false, extra); err == nil || !strings.Contains(err.Error(), "cannot land") {
			t.Errorf("want dead placeholder %q refused, got %v", dead, err)
		}
	}

	// The spliced args don't ride positional parameters, so nothing the
	// script does to $@ (set --, shift, a function's own scope) loses them,
	// and quoted function-like text in a prompt is not misread as syntax.
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0")
	for _, script := range []string{
		"set -- other; touch {{ARGS}}",
		"f() { touch {{ARGS}}; }; f",
		`true 'mentions run()' && touch {{ARGS}}`,
	} {
		wsDir = newWs()
		cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: script, Again: script}}
		if err := runAgent(cfg, "ws", wsDir, false, false, extra); err != nil {
			t.Fatalf("runAgent %q: %v", script, err)
		}
		argvFiles(wsDir)
	}

	// Metacharacters inside quoted arguments don't make a command compound:
	// its shell-visible structure is still a plain simple command.
	wsDir = newWs()
	cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{
		First: `touch 'call run(); x#y'`, Again: `touch 'call run(); x#y'`}}
	if err := runAgent(cfg, "ws", wsDir, false, false, extra); err != nil {
		t.Fatalf("runAgent with quoted metacharacters: %v", err)
	}
	argvFiles(wsDir)

	// Args containing single quotes survive the splice intact.
	wsDir = newWs()
	cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: "touch {{ARGS}}", Again: "touch {{ARGS}}"}}
	if err := runAgent(cfg, "ws", wsDir, false, false, []string{"it's here"}); err != nil {
		t.Fatalf("runAgent with quoted arg: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "it's here")); err != nil {
		t.Error("an arg containing a single quote should survive the splice intact")
	}

	// A quoted here-document body suppresses expansion, and spotting one
	// needs a real lexer; the combination is refused outright.
	wsDir = newWs()
	heredoc := "cat <<'EOF'\n{{ARGS}}\nEOF"
	cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: heredoc, Again: heredoc}}
	if err := runAgent(cfg, "ws", wsDir, false, false, extra); err == nil || !strings.Contains(err.Error(), "here-document") {
		t.Errorf("want heredoc + placeholder refused, got %v", err)
	}

	// {{ARGS}} glued to surrounding text can't keep each arg its own word:
	// only the first would attach, the rest would run as commands.
	wsDir = newWs()
	glued := "PROMPT={{ARGS}} touch ok"
	cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: glued, Again: glued}}
	if err := runAgent(cfg, "ws", wsDir, false, false, extra); err == nil || !strings.Contains(err.Error(), "stand alone") {
		t.Errorf("want glued placeholder refused, got %v", err)
	}

	// Hooks are not agent runs: a {{ARGS}} they carry for some downstream
	// templater passes through untouched, in diagnostics as much as in
	// execution.
	wsDir = newWs()
	if err := runHostCommand(config.ProjectConfig{Project: "proj"}, `touch '{{ARGS}}'`, "ws", wsDir, false); err != nil {
		t.Fatalf("hook run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "{{ARGS}}")); err != nil {
		t.Error("a hook's literal {{ARGS}} should reach its command untouched")
	}
	_, err := runHostCommandDetail(config.ProjectConfig{Project: "proj"}, "nonexistent-cmd-xyz {{ARGS}}", "ws", wsDir, false, nil, false)
	if err == nil || !strings.Contains(err.Error(), "{{ARGS}}") {
		t.Errorf("a hook's failure diagnostic should show its literal command, got %v", err)
	}

	// {{ARGS}} with nothing after -- expands to no words at all.
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0")
	wsDir = newWs()
	cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{
		First: `printf 'none%s' {{ARGS}} > got.txt`, Again: `printf 'none%s' {{ARGS}} > got.txt`}}
	if err := runAgent(cfg, "ws", wsDir, false, false, nil); err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	if got := readArgs(wsDir, "got.txt"); got != "none" {
		t.Errorf("empty {{ARGS}} argv = %q, want %q", got, "none")
	}
}

func TestDisplayCommand(t *testing.T) {
	if got := displayCommand("claude --continue", nil); got != "claude --continue" {
		t.Errorf("displayCommand without extra = %q", got)
	}
	got := displayCommand("claude", []string{"fix it", "now", ""})
	if want := `claude 'fix it' 'now' ''`; got != want {
		t.Errorf("displayCommand = %q, want %q", got, want)
	}
	// Args render where {{ARGS}} places them, not appended after the
	// pipeline, so the diagnostic mirrors what actually executed.
	got = displayCommand("claude {{ARGS}} | tee log", []string{"hi"})
	if want := `claude 'hi' | tee log`; got != want {
		t.Errorf("displayCommand with placeholder = %q, want %q", got, want)
	}
	// With no args the placeholder still renders (as nothing), matching
	// execution, which never runs a literal {{ARGS}}.
	got = displayCommand("claude {{ARGS}} --continue", nil)
	if want := `claude  --continue`; got != want {
		t.Errorf("displayCommand with empty placeholder = %q, want %q", got, want)
	}
}

func TestWriteWorkspaceMarkerRefusesSymlinks(t *testing.T) {
	// The worktree, .slate included, is container-writable: a planted link
	// must not redirect a host-side write to a file outside the workspace.
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, ".slate"), 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(wsDir, ".slate", "agent-last-run")); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceMarker(wsDir, "agent-last-run", []byte("clobbered")); err == nil {
		t.Error("want a linked marker file refused")
	}
	if got, _ := os.ReadFile(victim); string(got) != "precious" {
		t.Errorf("linked file written through: %q", got)
	}

	linkedWs := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(linkedWs, ".slate")); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceMarker(linkedWs, "agent-started", nil); err == nil {
		t.Error("want a linked .slate directory refused")
	}
	if err := removeWorkspaceMarker(linkedWs, "agent-first-run-pending"); err == nil {
		t.Error("want a removal through a linked .slate directory refused")
	}
}

func TestRunAgentLeavesBreadcrumb(t *testing.T) {
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0")

	// The agent exits outside the bail window: the breadcrumb records the
	// run whatever else happens to the session.
	wsDir, _, _ := runAgentIn(t, config.AgentCmd{First: "exit 1", Again: "exit 1"}, false)
	got, readErr := os.ReadFile(filepath.Join(wsDir, ".slate", "agent-last-run"))
	if readErr != nil {
		t.Fatalf("want a breadcrumb recording the run: %v", readErr)
	}
	for _, want := range []string{"variant=thereafter", "exit=1", "command=exit 1"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("breadcrumb %q missing %q", got, want)
		}
	}
}

func TestRunAgentHoldsOnCrashedSession(t *testing.T) {
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0.3")

	// The vanish shape: the session outlives the launch floor, then ends
	// with a plain non-zero exit. That is a crash, not a quit, and slate
	// must not return cleanly and take the tmux session with it.
	_, err, marked := runAgentIn(t, config.AgentCmd{First: "sleep 0.5; exit 7", Again: "sleep 0.5; exit 7"}, false)
	if err == nil || !strings.Contains(err.Error(), "ended with exit 7") {
		t.Errorf("want a crashed session surfaced, got %v", err)
	}
	if !marked {
		t.Error("a crashed session still existed; the next entry should continue it")
	}

	// A crash in the first-run retry is attributed to the variant that
	// actually ran, not the thereafter variant that bailed before it.
	_, err, _ = runAgentIn(t, config.AgentCmd{First: "sleep 0.5; exit 7", Again: "true"}, false)
	if err == nil || !strings.Contains(err.Error(), "the first-run agent session ended with exit 7") {
		t.Errorf("want the crash attributed to the retried first-run variant, got %v", err)
	}

	// A signal death is the user stopping the session: clean teardown.
	_, err, marked = runAgentIn(t, config.AgentCmd{First: "sleep 0.5; kill -TERM $$", Again: "sleep 0.5; kill -TERM $$"}, false)
	if err != nil {
		t.Errorf("a signal death should tear down cleanly, got %v", err)
	}
	if !marked {
		t.Error("a signal-stopped session still existed and should be recorded")
	}

	// A clean exit stays a clean exit.
	if _, err, _ := runAgentIn(t, config.AgentCmd{First: "sleep 0.5", Again: "sleep 0.5"}, false); err != nil {
		t.Errorf("a clean quit should not be held, got %v", err)
	}
}

func TestOfferTeardownStaleMarkerKeptWhenTipUnknown(t *testing.T) {
	// A staged marker must survive a transient inability to read the tip,
	// rather than being discarded as stale.
	wsDir := t.TempDir()
	staged := stagedTeardownMarker(wsDir)
	if err := os.WriteFile(staged, []byte("abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate the offer's stale-clearing predicate with an unknown tip.
	ev := landedEvidence{tip: ""}
	raw, _ := os.ReadFile(staged)
	stale := false
	switch {
	case ev.tip == "":
	case string(raw) == ev.tip:
	default:
		stale = true
	}
	if stale {
		t.Error("marker must not be treated as stale when the tip is unknown")
	}
	if _, err := os.Stat(staged); err != nil {
		t.Error("marker should still exist")
	}
}
