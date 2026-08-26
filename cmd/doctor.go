package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/dockernet"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check that all dependencies are in place",
	RunE:  runDoctor,
}

func runDoctor(cmd *cobra.Command, args []string) error {
	fmt.Println("Checking slate dependencies...")
	allOK := true

	check := func(ok bool, label string) {
		if ok {
			fmt.Printf("  "+tick()+" %s\n", label)
		} else {
			fmt.Printf("  "+cross()+" %s\n", label)
			allOK = false
		}
	}

	checkWarn := func(ok bool, label string) {
		if ok {
			fmt.Printf("  "+tick()+" %s\n", label)
		} else {
			fmt.Printf("  "+warn()+" %s\n", label)
		}
	}

	// Docker
	if v := cmdVersion("docker", "--version"); v != "" {
		check(true, fmt.Sprintf("docker (%s)", v))
	} else {
		check(false, "docker not found")
	}

	dockerUp := exec.Command("docker", "info").Run() == nil
	check(dockerUp, "docker daemon running")

	if dockerUp {
		reportNetworkPools(checkWarn)
	}

	// HTTPS proxy (slate container > standalone Caddy > Herd)
	proxyFound := false
	caddyAPIReady := false

	client := http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get("http://127.0.0.1:2019/config/"); err == nil {
		resp.Body.Close()
		caddyAPIReady = true
	}

	if isProxyRunning() {
		fmt.Println("  " + tick() + " slate proxy (Caddy container, will be used)")
		proxyFound = true
	} else if caddyAPIReady {
		fmt.Println("  " + tick() + " caddy (API reachable, will be used)")
		proxyFound = true
	}
	if v := cmdVersion("herd", "--version"); v != "" {
		label := fmt.Sprintf("herd (%s)", v)
		if proxyFound {
			label += " (available, Caddy preferred)"
		} else {
			label += " (will be used)"
			proxyFound = true
		}
		fmt.Printf("  "+tick()+" %s\n", label)
	}
	if !proxyFound {
		check(false, "no HTTPS proxy. Run `slate proxy start` or install Caddy/Herd.")
	}

	// Git
	if v := cmdVersion("git", "--version"); v != "" {
		check(true, fmt.Sprintf("git (%s)", v))
	} else {
		check(false, "git not found")
	}

	// tmux
	if v := cmdVersion("tmux", "-V"); v != "" {
		checkWarn(true, fmt.Sprintf("tmux (%s)", v))
	} else {
		checkWarn(false, "tmux not found (needed for slate attach)")
	}

	// DNS
	out, err := exec.Command("dig", "+short", "test.test", "@127.0.0.1").Output()
	dnsOK := err == nil && strings.Contains(string(out), "127.0.0.1")
	check(dnsOK, "*.test DNS resolves to 127.0.0.1")
	if !dnsOK {
		fmt.Println("     Run `slate dns start` (or `slate setup`).")
	}

	// Entrypoint
	entrypoint := config.DataDir() + "/entrypoint.sh"
	_, err = os.Stat(entrypoint)
	check(err == nil, fmt.Sprintf("slate entrypoint at %s", entrypoint))

	runProjectDoctorChecks(os.Stdout)

	// HTTPS port: report the port the proxy is actually serving on (which can
	// differ from config when the configured port was taken, e.g. Herd on 443),
	// falling back to the configured value when the proxy isn't running.
	_, httpsPort, _ := DetectProxyPorts()
	fmt.Printf("\n  HTTPS port: %d\n", httpsPort)

	fmt.Println()
	if allOK {
		fmt.Println("All good.")
	} else {
		fmt.Println("Some checks failed.")
	}
	return nil
}

func cmdVersion(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	// Extract version-like substring
	for _, word := range strings.Fields(v) {
		if len(word) > 0 && (word[0] >= '0' && word[0] <= '9' || word[0] == 'v') {
			return strings.TrimRight(word, ",")
		}
	}
	return v
}

// lowNetworkHeadroom is how few creatable networks counts as worth warning
// about. Every workspace needs one, so running this close means the next
// `slate new` or two is likely to fail.
const lowNetworkHeadroom = 3

// reportNetworkPools surfaces Docker's address-pool budget, which is the real
// ceiling on concurrent workspaces and is invisible until it fails: stock
// Docker Engine allows 32 networks, OrbStack 30. Idle ones are counted as
// headroom because `slate down` and a failed `slate up` both reclaim them.
func reportNetworkPools(checkWarn func(bool, string)) {
	inUse, capacity, err := dockernet.Pools()
	if err != nil || capacity == 0 {
		return
	}

	idle, _ := dockernet.Idle()
	label := fmt.Sprintf("docker address pools (%d of %d networks in use", inUse, capacity)
	if len(idle) > 0 {
		label += fmt.Sprintf(", %d reclaimable", len(idle))
	}
	label += ")"

	free := capacity - inUse + len(idle)
	checkWarn(free >= lowNetworkHeadroom, label)

	if free < lowNetworkHeadroom {
		fmt.Println("     Nearly out of networks; the next `slate new` may fail to start.")
		fmt.Println("     Raise the ceiling with default-address-pools in the Docker daemon config.")
	}
	if len(idle) > 0 {
		fmt.Printf("     %d idle %s can be reclaimed: `slate down` sweeps them, or `docker network prune`.\n",
			len(idle), plural(len(idle), "network", "networks"))
	}
}

// hookTimeout bounds each doctor:/brief: hook run so a hung command can't
// wedge the CLI. A timed-out check renders as a warning.
var hookTimeout = 10 * time.Second

type hookOutcome struct {
	output   string
	exitCode int
	timedOut bool
}

// hookSessionVars are the per-session slate vars an enclosing agent session
// exports. Hooks must see only the slate context their own invocation
// defines: `slate doctor` run inside an agent session would otherwise hand
// its checks that session's SLATE_WORKSPACE despite doctor having no
// workspace context at all.
var hookSessionVars = []string{"SLATE_PROJECT", "SLATE_WORKSPACE", "SLATE_FRESH", "SLATE_PROVISIONING", "SLATE_AGENT"}

// runCapturedHook runs a doctor:/brief: hook via sh -c in dir. Doctor checks
// capture combined output (combined=true); brief captures stdout alone and
// lets stderr flow through so diagnostics stay out of the markdown.
func runCapturedHook(command, dir string, env []string, combined bool) (hookOutcome, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", command)
	c.Dir = dir
	// the hook gets its own process group and the timeout kills the whole
	// group: killing just sh would leave its children running on the host
	// after slate reports the hook timed out
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	c.Env = os.Environ()
	c.Env = slices.DeleteFunc(c.Env, func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(hookSessionVars, name)
	})
	c.Env = append(c.Env, env...)
	var buf bytes.Buffer
	c.Stdout = &buf
	if combined {
		c.Stderr = &buf
	} else {
		c.Stderr = os.Stderr
	}
	// a hook's orphaned children can hold the output pipe open past the
	// timeout kill; don't wait on them forever
	c.WaitDelay = time.Second
	err := c.Run()
	out := hookOutcome{output: buf.String()}
	if ctx.Err() == context.DeadlineExceeded {
		out.timedOut = true
		return out, nil
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			out.exitCode = ee.ExitCode()
			return out, nil
		}
		return out, err
	}
	return out, nil
}

// runProjectDoctorChecks appends the project's `doctor:` checks to the
// built-in output. Doctor runs fine outside any project, so no project
// context just means no checks.
func runProjectDoctorChecks(out io.Writer) {
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return
	}
	// checks never get workspace context, but a doctor run from inside a
	// workspace still owes the inert-edit note for worktree copies of the
	// host-exec keys or a worktree slate.local.yml
	if _, wsDir, err := workspace.ResolveWorkspace(); err == nil {
		warnIfHostExecEditsInert(mainRoot, wsDir)
	}
	cfg, err := config.LoadMainProject(mainRoot)
	if err != nil {
		fmt.Fprintf(out, "\n  %s project config: %v\n", warn(), err)
		return
	}
	if len(cfg.Doctor) == 0 {
		return
	}
	project, err := workspace.ProjectName(cfg.Project)
	if err != nil {
		fmt.Fprintf(out, "\n  %s project checks skipped: %v\n", warn(), err)
		return
	}
	fmt.Fprintln(out, "\nProject checks...")
	printProjectDoctorChecks(out, cfg.Doctor, mainRoot, project)
}

// printProjectDoctorChecks runs each named check via sh -c in the main
// checkout, sorted by name (YAML map order isn't reliable). Doctor has no
// workspace context: only {{PROJECT}} expands ({{WORKSPACE}}/{{HOSTNAME}}
// stay literal) and SLATE_WORKSPACE is unset. Failures render as warnings
// with the check's combined output and never affect doctor's own result.
func printProjectDoctorChecks(out io.Writer, checks map[string]string, mainRoot, project string) {
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		command := strings.ReplaceAll(checks[name], "{{PROJECT}}", project)
		res, err := runCapturedHook(command, mainRoot, []string{"SLATE_PROJECT=" + project}, true)
		switch {
		case err != nil:
			fmt.Fprintf(out, "  %s %s (could not run: %v)\n", warn(), name, err)
		case res.timedOut:
			fmt.Fprintf(out, "  %s %s (timed out after %s)\n", warn(), name, hookTimeout)
		case res.exitCode != 0:
			fmt.Fprintf(out, "  %s %s (exit %d)\n", warn(), name, res.exitCode)
			if text := strings.TrimSpace(res.output); text != "" {
				for _, line := range strings.Split(text, "\n") {
					fmt.Fprintf(out, "     %s\n", line)
				}
			}
		default:
			fmt.Fprintf(out, "  %s %s\n", tick(), name)
		}
	}
}
