package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devtime-ltd/slate/internal/config"
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
		bare     bool
		freshEnv string
		want     bool
	}{
		{"first entry via up hook", false, false, "1", true},
		{"first entry in bare workspace", false, true, "", true},
		{"existing pre-marker workspace", false, false, "", false},
		{"marker beats SLATE_FRESH", true, false, "1", false},
		{"marker beats bare", true, true, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wsDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(wsDir, ".slate"), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.marker {
				if err := os.WriteFile(agentStartedMarker(wsDir), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.bare {
				if err := os.WriteFile(unprovisionedMarker(wsDir), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("SLATE_FRESH", tc.freshEnv)
			if got := agentFresh(wsDir); got != tc.want {
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

// runAgentIn runs the agent in a throwaway workspace and reports the error
// alongside whether the entry was recorded.
func runAgentIn(t *testing.T, agent config.AgentCmd, fresh bool) (string, error, bool) {
	t.Helper()
	// so a failing runAgent can never hold the test hostage with a shell,
	// however the suite is invoked
	agentNoHold = true
	t.Cleanup(func() { agentNoHold = false })
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, ".slate"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.ProjectConfig{Project: "proj", Agent: agent}
	err := runAgent(cfg, "ws", wsDir, fresh)
	_, statErr := os.Stat(agentStartedMarker(wsDir))
	return wsDir, err, statErr == nil
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
	wsDir, _, _ := runAgentIn(t, config.AgentCmd{First: "true", Again: "sleep 0.5"}, true)
	if !agentFresh(wsDir) {
		t.Error("a failed first-run launch should leave the workspace fresh for the next entry")
	}

	// A bailed thereafter run whose first-run retry also bails owes the
	// workspace a first-run entry too.
	wsDir, _, _ = runAgentIn(t, config.AgentCmd{First: "true", Again: "exit 1"}, false)
	if !agentFresh(wsDir) {
		t.Error("a failed first-run retry should leave the workspace fresh for the next entry")
	}

	// A session that runs settles the debt.
	wsDir, _, _ = runAgentIn(t, config.AgentCmd{First: "sleep 0.5", Again: "true"}, true)
	if _, err := os.Stat(firstRunPendingMarker(wsDir)); err == nil {
		t.Error("a session that ran should clear the pending first-run marker")
	}
	if agentFresh(wsDir) {
		t.Error("a session that ran should end the workspace's freshness")
	}

	// A plain thereafter failure with no retry configured owes nothing.
	wsDir, _, _ = runAgentIn(t, config.AgentCmd{First: "true", Again: "true"}, false)
	if agentFresh(wsDir) {
		t.Error("a failed thereafter launch with no distinct first-run variant should not mark the workspace fresh")
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

	// The vanish shape: the agent exits outside the bail window, slate
	// returns cleanly, and the enclosing tmux session dies. The breadcrumb
	// is the only evidence left in the workspace.
	wsDir, err, _ := runAgentIn(t, config.AgentCmd{First: "exit 1", Again: "exit 1"}, false)
	if err != nil {
		t.Fatalf("an ordinary non-zero exit is not a slate failure, got %v", err)
	}
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
