package cmd

import (
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPrintProjectDoctorChecks(t *testing.T) {
	var b strings.Builder
	checks := map[string]string{
		"c-pass":  "test \"$SLATE_PROJECT\" = proj",
		"a-fail":  "echo broken thing; exit 3",
		"b-quiet": "true",
	}
	printProjectDoctorChecks(&b, checks, t.TempDir(), "proj")
	out := b.String()

	if !strings.Contains(out, "a-fail (exit 3)") {
		t.Errorf("failing check should render its name and exit code:\n%s", out)
	}
	if !strings.Contains(out, "     broken thing") {
		t.Errorf("failing check should render its combined output indented:\n%s", out)
	}
	if !strings.Contains(out, "c-pass") || !strings.Contains(out, "b-quiet") {
		t.Errorf("passing checks should render their names:\n%s", out)
	}
	if strings.Contains(out, "c-pass (exit") {
		t.Errorf("SLATE_PROJECT should be set for doctor checks:\n%s", out)
	}
	// deterministic order: sorted by name, not YAML map order
	if !(strings.Index(out, "a-fail") < strings.Index(out, "b-quiet") &&
		strings.Index(out, "b-quiet") < strings.Index(out, "c-pass")) {
		t.Errorf("checks should render sorted by name:\n%s", out)
	}
}

func TestPrintProjectDoctorChecksExpandsProject(t *testing.T) {
	var b strings.Builder
	printProjectDoctorChecks(&b, map[string]string{
		"name": `test "{{PROJECT}}" = proj`,
	}, t.TempDir(), "proj")
	if strings.Contains(b.String(), "exit") {
		t.Errorf("{{PROJECT}} should expand in doctor checks:\n%s", b.String())
	}
}

func TestPrintProjectDoctorChecksTimeout(t *testing.T) {
	old := hookTimeout
	hookTimeout = 100 * time.Millisecond
	defer func() { hookTimeout = old }()

	var b strings.Builder
	printProjectDoctorChecks(&b, map[string]string{"slow": "sleep 5"}, t.TempDir(), "proj")
	if !strings.Contains(b.String(), "slow (timed out after 100ms)") {
		t.Errorf("a hung check should render as a timed-out warning:\n%s", b.String())
	}
}

func TestHookDoesNotInheritSessionEnv(t *testing.T) {
	t.Setenv("SLATE_WORKSPACE", "leaky")
	t.Setenv("SLATE_FRESH", "1")

	var b strings.Builder
	printProjectDoctorChecks(&b, map[string]string{
		"no-session-env": `test -z "$SLATE_WORKSPACE" && test -z "$SLATE_FRESH"`,
	}, t.TempDir(), "proj")
	if strings.Contains(b.String(), "exit") {
		t.Errorf("doctor checks must not inherit an enclosing session's SLATE_* vars:\n%s", b.String())
	}
}

func TestPrintProjectDoctorChecksSilentFailure(t *testing.T) {
	var b strings.Builder
	printProjectDoctorChecks(&b, map[string]string{"silent": "exit 2"}, t.TempDir(), "proj")
	want := "  " + warn() + " silent (exit 2)\n"
	if b.String() != want {
		t.Errorf("a failing check with no output should render only its warning line, got %q", b.String())
	}
}

func TestHookTimeoutKillsProcessGroup(t *testing.T) {
	old := hookTimeout
	hookTimeout = 100 * time.Millisecond
	defer func() { hookTimeout = old }()

	res, err := runCapturedHook("sleep 30 & echo $!; wait", t.TempDir(), nil, true)
	if err != nil || !res.timedOut {
		t.Fatalf("expected a timeout, got %+v, %v", res, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(res.output))
	if err != nil {
		t.Fatalf("hook output should be the child pid, got %q", res.output)
	}
	// the backgrounded child must die with the group, not linger on the host
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return // ESRCH: the child is gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Error("the hook's child process survived the timeout")
}
