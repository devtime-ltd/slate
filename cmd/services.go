package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/devtime-ltd/slate/internal/assets"
	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/dockernet"
	"github.com/devtime-ltd/slate/internal/proxy"
	"github.com/devtime-ltd/slate/internal/scaffold"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

// workspaceConfigNote flags when the (authoritative) worktree slate.yml
// differs from the main checkout's.
func workspaceConfigNote(mainRoot, wsDir string) string {
	if mainRoot == "" || wsDir == "" || mainRoot == wsDir {
		return ""
	}
	wsData, err := os.ReadFile(filepath.Join(wsDir, "slate.yml"))
	if err != nil {
		return ""
	}
	mainData, _ := os.ReadFile(filepath.Join(mainRoot, "slate.yml"))
	if bytes.Equal(bytes.TrimSpace(wsData), bytes.TrimSpace(mainData)) {
		return ""
	}
	return "  note: using this workspace's slate.yml (differs from the main checkout's)"
}

func warnIfWorkspaceConfigDiffers(mainRoot, wsDir string) {
	if note := workspaceConfigNote(mainRoot, wsDir); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	if keys := config.TrustPinned(mainRoot, wsDir); len(keys) > 0 {
		fmt.Fprintf(os.Stderr, "  note: this workspace's slate.yml changes `%s`; these come from committed content on this branch (or the main checkout), so commit the change here for it to take effect\n", strings.Join(keys, "` and `"))
	}
	warnIfHostExecEditsInert(mainRoot, wsDir)
}

// warnIfHostExecEditsInert covers just the host-exec notes, for commands
// (like `slate brief`) that never use the worktree's config and where the
// broader worktree-config notes would be untrue.
func warnIfHostExecEditsInert(mainRoot, wsDir string) {
	if keys := config.HostExecPinned(mainRoot, wsDir); len(keys) > 0 {
		fmt.Fprintf(os.Stderr, "  note: this workspace's slate.yml changes `%s`; host commands always come from the main checkout's slate.yml, so the change has no effect until it lands there\n", strings.Join(keys, "` and `"))
	}
	if wsDir != "" && mainRoot != wsDir {
		// any entry by this name is equally inert, so a directory or broken
		// symlink gets the note too
		if _, err := os.Lstat(filepath.Join(wsDir, config.LocalConfigName)); err == nil {
			fmt.Fprintf(os.Stderr, "  note: this workspace has a %s; local overrides are only read from the main checkout root, so it has no effect\n", config.LocalConfigName)
		}
	}
}

// resolveAutoCd returns the explicit flag value if the user passed --cd /
// --cd=false. Otherwise it falls back to the global config's auto_cd default,
// but never auto-spawns a shell when stdio isn't an interactive terminal (so
// agents, CI, and piped invocations don't block).
func resolveAutoCd(cmd *cobra.Command, flagName string, flagVal bool) bool {
	if cmd.Flags().Changed(flagName) {
		return flagVal
	}
	if !isInteractiveTerminal() {
		return false
	}
	cfg, _ := config.LoadGlobal()
	return cfg.AutoCd
}

// isInteractiveTerminal reports whether both stdin and stdout are character
// devices (i.e. a real TTY), rather than pipes or files.
func isInteractiveTerminal() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

// provisionOpts captures the variations between fresh-workspace creation,
// idempotent up, and the bg worker re-invocation.
type provisionOpts struct {
	fresh bool
	build bool
	wipe  bool
}

func lifecycleLabel(fresh bool) string {
	if fresh {
		return "up + new"
	}
	return "up"
}

// runWorkspaceLifecycle runs the slow phase of bringing a workspace up:
// optional wipe, compose up, lifecycle script (workers paused around it),
// proxy register, then prints the success message + URL block. Shared by
// runNew, runUp, and the bg _provision worker. Manages the .slate/provisioning
// lockfile so concurrent `slate ls` calls see the in-flight status, and any
// stale .failed marker is cleared on a successful run.
func runWorkspaceLifecycle(env compose.Env, name, wsDir, hostname string, cfg config.ProjectConfig, proxyConfig config.GlobalConfig, opts provisionOpts) (retErr error) {
	cleanup := writeProvisioningLock(wsDir)
	defer func() { cleanup(retErr) }()

	if opts.wipe {
		fmt.Printf("Wiping containers and volumes for %s...\n", hostname)
		if err := compose.Run(env, "down", "-v", "--remove-orphans"); err != nil {
			return fmt.Errorf("compose down failed: %w", err)
		}
	}

	// Keep the deployed entrypoint (mounted into the containers) in sync with
	// this binary on every up, not only on setup.
	if _, err := assets.EnsureEntrypoint(); err != nil {
		return fmt.Errorf("installing entrypoint: %w", err)
	}

	fmt.Printf("Starting containers for %s...\n", hostname)
	// --remove-orphans: drop containers for services no longer in the
	// generated compose file (e.g. after a scaffold or config change)
	upArgs := []string{"up", "-d", "--remove-orphans"}
	if opts.build {
		upArgs = append(upArgs, "--build")
	}
	if err := compose.Run(env, upArgs...); err != nil {
		// Docker's address pools cap how many networks can exist at once, and
		// workspaces stopped outside slate strand theirs. Reclaim those and
		// retry, but only when something was actually freed, so an unrelated
		// failure isn't retried for no reason.
		if freed := dockernet.Reclaim(); len(freed) > 0 {
			fmt.Printf("Reclaimed %d idle workspace %s; retrying...\n",
				len(freed), plural(len(freed), "network", "networks"))
			err = compose.Run(env, upArgs...)
		}
		if err != nil {
			return fmt.Errorf("compose up failed: %w%s", err, networkPoolHint())
		}
	}

	workers := workerServices(env, cfg)
	if lifecycleScript := scaffold.BuildLifecycleScript(cfg, opts.fresh); lifecycleScript != "" {
		if err := runLifecycleScript(env, workers, lifecycleScript, opts.fresh); err != nil {
			return err
		}
	} else if len(workers) > 0 {
		// workers don't hot-reload
		warnOnWorkerStart(compose.Run(env, append([]string{"restart"}, workers...)...))
	}

	// Clear the bare marker the moment the lifecycle lands, not at the end:
	// if a later step like proxy registration failed, a retry `slate up`
	// would re-run the fresh lifecycle over data the user may have changed
	// while debugging. Failing here matters too: while the marker survives,
	// every plain `slate up` reruns the fresh wipe.
	if err := os.Remove(unprovisionedMarker(wsDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not clear the unprovisioned marker: %w\n\nRemove %s manually; while it exists every `slate up` reruns the fresh lifecycle (database wipe)", err, unprovisionedMarker(wsDir))
	}

	services := buildServicePorts(env, cfg)
	if err := proxy.Register(hostname, services); err != nil {
		return fmt.Errorf("proxy registration failed: %w", err)
	}

	fmt.Println()
	fmt.Println(tick() + " " + name + " ready")
	fmt.Println()
	fmt.Println(workspaceURLBlock(env, hostname, cfg, proxyConfig))
	return nil
}

// runLifecycleScript runs the script with the worker services stopped (a live
// queue worker deadlocks against its migrations), holding SIGINT until they
// are started again.
func runLifecycleScript(env compose.Env, workers []string, script string, fresh bool) error {
	if len(workers) > 0 {
		release := holdInterrupts()
		defer release()
		defer func() {
			warnOnWorkerStart(compose.Run(env, append([]string{"start"}, workers...)...))
		}()
		if err := compose.Run(env, append([]string{"stop"}, workers...)...); err != nil {
			return fmt.Errorf("pausing worker services for the lifecycle: %w", err)
		}
	}
	fmt.Printf("Running lifecycle (%s)...\n", lifecycleLabel(fresh))
	if err := compose.Exec(env, "app", "sh", "-c", script); err != nil {
		return fmt.Errorf("lifecycle failed: %w", err)
	}
	return nil
}

// checkNotProvisioning errors out if a live bg provision is in flight for
// the workspace. Used by up/restart to avoid racing the bg worker.
func checkNotProvisioning(wsDir string) error {
	pid, alive := readProvisioningLock(wsDir)
	if !alive {
		return nil
	}
	return fmt.Errorf("provisioning in flight (pid %d). `slate wait` blocks until it finishes; `slate rm` aborts it.\nLog: %s", pid, provisionLogPath(wsDir))
}

// killProvisioningLock signals SIGTERM to a live bg provisioner and removes
// the lockfile. Used by rm as an escape hatch.
func killProvisioningLock(wsDir string) {
	pid, alive := readProvisioningLock(wsDir)
	lockPath := filepath.Join(wsDir, ".slate", "provisioning")
	if alive {
		fmt.Printf("Stopping in-flight provisioner (pid %d)...\n", pid)
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
	}
	_ = os.Remove(lockPath)
}

// readProvisioningLock returns the pid from .slate/provisioning and whether
// that process is still alive. Returns (0, false) if no lock exists.
func readProvisioningLock(wsDir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(wsDir, ".slate", "provisioning"))
	if err != nil {
		return 0, false
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid <= 0 {
		return 0, false
	}
	return pid, processAlive(pid)
}

// writeProvisioningLock writes .slate/provisioning with the current pid and
// clears any stale .failed marker. Returns a cleanup func that, on success,
// removes the lock; on error, renames it to .failed.
func writeProvisioningLock(wsDir string) func(error) {
	lockPath := filepath.Join(wsDir, ".slate", "provisioning")
	failPath := filepath.Join(wsDir, ".slate", "provisioning.failed")
	if err := os.Remove(failPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "  warning: could not clear stale .failed marker: %v\n", err)
	}
	// Publish via rename: WriteFile truncates in place, and a reader hitting
	// that window would see an empty lock and treat mid-provision as ready.
	if err := writeFileAtomic(lockPath, []byte(fmt.Sprintf("%d\n", os.Getpid()))); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not write provisioning lock: %v\n", err)
	}
	return func(retErr error) {
		if retErr != nil {
			if err := os.Rename(lockPath, failPath); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: could not mark provisioning as failed: %v\n", err)
			}
			return
		}
		err := os.Remove(lockPath)
		if err == nil || os.IsNotExist(err) {
			return
		}
		// An unremovable lock (dir-permission oddities) would read as
		// "provisioner died" forever and block exec/wait after a successful
		// provision. In-place file writes can still work when unlink doesn't,
		// and readers treat a pid-0 lock as no lock. O_NOFOLLOW: the worktree
		// is container-writable, and this fallback must not become a write
		// primitive through a planted symlink.
		f, oerr := os.OpenFile(lockPath, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
		if oerr != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not remove provisioning lock: %v\n", err)
			return
		}
		if _, werr := f.Write([]byte("0\n")); werr != nil {
			fmt.Fprintf(os.Stderr, "  warning: could not remove provisioning lock: %v\n", err)
		}
		f.Close()
	}
}

// writeFileAtomic publishes content via a temp file + rename so concurrent
// readers never observe a truncated half-write. The temp name is unique per
// writer: the bg parent and its worker both publish the lock, and a shared
// temp path would let one writer's rename steal the other's file.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// workerServices: the app-like services beyond the primary (e.g. queue).
func workerServices(env compose.Env, cfg config.ProjectConfig) []string {
	if appLike := appLikeServices(env, cfg); len(appLike) > 1 {
		return appLike[1:]
	}
	return nil
}

// pauseWorkers stops the currently-running worker services, holding SIGINT,
// and returns a restore func that starts exactly those again and releases the
// hold, so a worker the user had stopped themselves stays stopped. The service
// the command runs in cannot be one of them.
func pauseWorkers(env compose.Env, wsDir, service string) (func(), error) {
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return nil, err
	}
	cfg, err := config.LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		return nil, err
	}
	workers := workerServices(env, cfg)
	if slices.Contains(workers, service) {
		return nil, fmt.Errorf("--pause-workers would stop %s, the service the command runs in", service)
	}
	if len(workers) == 0 {
		return func() {}, nil
	}
	live, err := compose.LiveServices(env)
	if err != nil {
		return nil, err
	}
	paused := liveWorkers(workers, live)
	if len(paused) == 0 {
		return func() {}, nil
	}
	release := holdInterrupts()
	restore := func() {
		warnOnWorkerStart(compose.Run(env, append([]string{"start"}, paused...)...))
		release()
	}
	// an interrupted stop may already have stopped some of them
	if err := compose.Run(env, append([]string{"stop"}, paused...)...); err != nil {
		restore()
		return nil, fmt.Errorf("stopping worker services: %w", err)
	}
	return restore, nil
}

// liveWorkers keeps the workers that are in the live set, in worker order.
func liveWorkers(workers, live []string) []string {
	var out []string
	for _, w := range workers {
		if slices.Contains(live, w) {
			out = append(out, w)
		}
	}
	return out
}

func warnOnWorkerStart(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "  warning: the worker services were not started again (%v); `slate up` starts them\n", err)
	}
}

// appLikeServices: static for built-in scaffolds, compose-derived for inline
// ones (signalled by a nil AppLikeServices).
func appLikeServices(env compose.Env, cfg config.ProjectConfig) []string {
	s, err := scaffold.Resolve(cfg)
	if err != nil {
		return nil
	}
	if fixed := s.AppLikeServices(); fixed != nil {
		return fixed
	}
	derived, err := compose.AppLikeServices(env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not derive /app-bind services: %v\n", err)
		return nil
	}
	return derived
}

// buildServicePorts queries running ports for each subdomain the scaffold exposes.
func buildServicePorts(env compose.Env, cfg config.ProjectConfig) proxy.ServicePorts {
	services := proxy.ServicePorts{}

	s, err := scaffold.Resolve(cfg)
	if err != nil {
		return services
	}

	for subdomain, def := range s.Subdomains() {
		port, err := compose.Port(env, def.Service, def.Port)
		if err == nil && port != "" {
			services[subdomain] = port
		}
	}
	return services
}

// workspaceURLBlock is the block form (main URL, indented sub-URLs) printed
// by the new/up/restart success messages.
func workspaceURLBlock(env compose.Env, hostname string, cfg config.ProjectConfig, globalCfg config.GlobalConfig) string {
	portFor := func(service string, containerPort int) string {
		port, err := compose.Port(env, service, containerPort)
		if err != nil {
			return ""
		}
		return port
	}
	main, subs := workspaceURLs(portFor, hostname, cfg, globalCfg)
	out := main
	for _, sub := range subs {
		out += "\n  " + sub
	}
	return out
}

// workspaceURLs returns the styled main URL and unindented sub-service
// lines (vite, mailpit, mysql, postgres); callers control placement.
// portFor resolves a service's published host port ("" when absent) — ls
// feeds it from one docker ps snapshot, up/restart from compose.Port.
func workspaceURLs(portFor func(service string, containerPort int) string, hostname string, cfg config.ProjectConfig, globalCfg config.GlobalConfig) (string, []string) {
	main := urlStyle.Render(globalCfg.WorkspaceURL(hostname))
	var lines []string

	if def := cfg.Scaffold.Inline; def != nil {
		var subs []string
		for sub := range def.Subdomains {
			if sub != "" {
				subs = append(subs, sub)
			}
		}
		sort.Strings(subs)
		for _, sub := range subs {
			d := def.Subdomains[sub]
			if portFor(d.Service, d.Port) != "" {
				lines = append(lines, subURLLine(sub, globalCfg.ServiceURL(sub, hostname)))
			}
		}
		return main, lines
	}

	if portFor("vite", cfg.VitePort) != "" {
		lines = append(lines, subURLLine("vite", globalCfg.ServiceURL("vite", hostname)))
	}
	if portFor("mailpit", 8025) != "" {
		lines = append(lines, subURLLine("mailpit", globalCfg.ServiceURL("mailpit", hostname)))
	}
	if mysqlPort := portFor("mysql", 3306); mysqlPort != "" {
		lines = append(lines, subURLLine("mysql", fmt.Sprintf("%s.test:%s", hostname, mysqlPort)))
	}
	if pgPort := portFor("postgres", 5432); pgPort != "" {
		lines = append(lines, subURLLine("postgres", fmt.Sprintf("%s.test:%s", hostname, pgPort)))
	}

	return main, lines
}

func subURLLine(label, url string) string {
	return dimStyle.Render("↳ "+label+": ") + fadedStyle.Render(url)
}
