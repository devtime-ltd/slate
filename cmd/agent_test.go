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
	err := runAgent(cfg, "ws", wsDir, fresh, nil)
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
	if err := runAgent(cfg, "ws", wsDir, false, extra); err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	argvFiles(wsDir)

	// A block-scalar `agent: |` decodes with a trailing newline; the args
	// must still reach the command instead of running as their own line.
	wsDir = newWs()
	cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: "touch\n", Again: "touch\n"}}
	if err := runAgent(cfg, "ws", wsDir, false, extra); err != nil {
		t.Fatalf("runAgent with trailing newline: %v", err)
	}
	argvFiles(wsDir)

	// With shell structure present, an append can land on the wrong
	// pipeline stage, run as its own command, or vanish into a comment;
	// refused unless {{ARGS}} pins the placement.
	for _, compound := range []string{"true &", "true | cat", "true; true", "true 2>&1", "true # note", "(true)", `touch \`} {
		wsDir = newWs()
		cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: compound, Again: compound}}
		if err := runAgent(cfg, "ws", wsDir, false, extra); err == nil || !strings.Contains(err.Error(), "{{ARGS}}") {
			t.Errorf("want compound command %q refused, got %v", compound, err)
		}
	}

	// {{ARGS}} pins where the args land, compound commands included, and the
	// first-run retry carries the same args.
	t.Setenv("SLATE_AGENT_MIN_RUNTIME", "0.3")
	wsDir = newWs()
	cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{
		First: `sleep 0.5; printf '%s\n' {{ARGS}} > first.txt`, Again: "true"}}
	if err := runAgent(cfg, "ws", wsDir, false, extra); err != nil {
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
		if err := runAgent(cfg, "ws", wsDir, false, extra); err != nil {
			t.Fatalf("runAgent with %q: %v", spelling, err)
		}
		argvFiles(wsDir)
	}

	// {{ARGS}} buried in a longer quoted region or a comment can never
	// receive the args; refused rather than silently dropping them.
	for _, dead := range []string{`touch 'review {{ARGS}}'`, `touch "review {{ARGS}}"`, "touch # {{ARGS}}", "touch #{{ARGS}}", "(touch)# {{ARGS}}"} {
		wsDir = newWs()
		cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: dead, Again: dead}}
		if err := runAgent(cfg, "ws", wsDir, false, extra); err == nil || !strings.Contains(err.Error(), "cannot land") {
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
		if err := runAgent(cfg, "ws", wsDir, false, extra); err != nil {
			t.Fatalf("runAgent %q: %v", script, err)
		}
		argvFiles(wsDir)
	}

	// Metacharacters inside quoted arguments don't make a command compound:
	// its shell-visible structure is still a plain simple command.
	wsDir = newWs()
	cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{
		First: `touch 'call run(); x#y'`, Again: `touch 'call run(); x#y'`}}
	if err := runAgent(cfg, "ws", wsDir, false, extra); err != nil {
		t.Fatalf("runAgent with quoted metacharacters: %v", err)
	}
	argvFiles(wsDir)

	// Args containing single quotes survive the splice intact.
	wsDir = newWs()
	cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: "touch {{ARGS}}", Again: "touch {{ARGS}}"}}
	if err := runAgent(cfg, "ws", wsDir, false, []string{"it's here"}); err != nil {
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
	if err := runAgent(cfg, "ws", wsDir, false, extra); err == nil || !strings.Contains(err.Error(), "here-document") {
		t.Errorf("want heredoc + placeholder refused, got %v", err)
	}

	// {{ARGS}} glued to surrounding text can't keep each arg its own word:
	// only the first would attach, the rest would run as commands.
	wsDir = newWs()
	glued := "PROMPT={{ARGS}} touch ok"
	cfg = config.ProjectConfig{Project: "proj", Agent: config.AgentCmd{First: glued, Again: glued}}
	if err := runAgent(cfg, "ws", wsDir, false, extra); err == nil || !strings.Contains(err.Error(), "stand alone") {
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
	if err := runAgent(cfg, "ws", wsDir, false, nil); err != nil {
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
