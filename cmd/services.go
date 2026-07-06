package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/devtime-ltd/slate/internal/assets"
	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/proxy"
	"github.com/devtime-ltd/slate/internal/scaffold"
	"github.com/spf13/cobra"
)

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
// optional wipe, compose up, lifecycle script, queue restart, proxy register,
// then prints the success message + URL block. Shared by runNew, runUp,
// and the bg _provision worker. Manages the .slate/provisioning lockfile so
// concurrent `slate ls` calls see the in-flight status, and any stale
// .failed marker is cleared on a successful run.
func runWorkspaceLifecycle(env compose.Env, name, wsDir, hostname string, cfg config.ProjectConfig, proxyConfig config.GlobalConfig, opts provisionOpts) (retErr error) {
	cleanup := writeProvisioningLock(wsDir)
	defer func() { cleanup(retErr) }()

	if opts.wipe {
		fmt.Printf("Wiping containers and volumes for %s...\n", hostname)
		if err := compose.Run(env, "down", "-v"); err != nil {
			return fmt.Errorf("compose down failed: %w", err)
		}
	}

	// Keep the deployed entrypoint (mounted into the containers) in sync with
	// this binary on every up, not only on setup.
	if _, err := assets.EnsureEntrypoint(); err != nil {
		return fmt.Errorf("installing entrypoint: %w", err)
	}

	fmt.Printf("Starting containers for %s...\n", hostname)
	upArgs := []string{"up", "-d"}
	if opts.build {
		upArgs = append(upArgs, "--build")
	}
	if err := compose.Run(env, upArgs...); err != nil {
		return fmt.Errorf("compose up failed: %w", err)
	}

	if lifecycleScript := scaffold.BuildLifecycleScript(cfg, opts.fresh); lifecycleScript != "" {
		fmt.Printf("Running lifecycle (%s)...\n", lifecycleLabel(opts.fresh))
		if err := compose.Exec(env, "app", "sh", "-c", lifecycleScript); err != nil {
			return fmt.Errorf("lifecycle failed: %w", err)
		}
	}

	// Restart worker services (app-like beyond the primary); they don't hot-reload.
	if s, err := scaffold.Get(cfg.Scaffold); err == nil {
		if appLike := s.AppLikeServices(); len(appLike) > 1 {
			for _, svc := range appLike[1:] {
				_ = compose.Run(env, "restart", svc)
			}
		}
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

// checkNotProvisioning errors out if a live bg provision is in flight for
// the workspace. Used by up/restart to avoid racing the bg worker.
func checkNotProvisioning(wsDir string) error {
	pid, alive := readProvisioningLock(wsDir)
	if !alive {
		return nil
	}
	logPath := filepath.Join(wsDir, ".slate", "provision.log")
	return fmt.Errorf("provisioning in flight (pid %d). Wait for it to finish, or `slate rm` to abort.\nLog: %s", pid, logPath)
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
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not write provisioning lock: %v\n", err)
	}
	return func(retErr error) {
		if retErr != nil {
			if err := os.Rename(lockPath, failPath); err != nil {
				fmt.Fprintf(os.Stderr, "  warning: could not mark provisioning as failed: %v\n", err)
			}
		} else {
			if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "  warning: could not remove provisioning lock: %v\n", err)
			}
		}
	}
}

// buildServicePorts queries running ports for each subdomain the scaffold exposes.
func buildServicePorts(env compose.Env, cfg config.ProjectConfig) proxy.ServicePorts {
	services := proxy.ServicePorts{}

	s, err := scaffold.Get(cfg.Scaffold)
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

// workspaceURLBlock returns the main URL with sub-URLs (vite, mailpit, mysql, postgres)
// indented underneath. Used by ls and the new/up/restart success messages.
func workspaceURLBlock(env compose.Env, hostname string, cfg config.ProjectConfig, globalCfg config.GlobalConfig) string {
	out := globalCfg.WorkspaceURL(hostname)

	if vitePort, err := compose.Port(env, "vite", cfg.VitePort); err == nil && vitePort != "" {
		out += "\n" + dimStyle.Render("  ↳ vite: ") + globalCfg.ServiceURL("vite", hostname)
	}
	if _, err := compose.Port(env, "mailpit", 8025); err == nil {
		out += "\n" + dimStyle.Render("  ↳ mailpit: ") + globalCfg.ServiceURL("mailpit", hostname)
	}
	if mysqlPort, err := compose.Port(env, "mysql", 3306); err == nil && mysqlPort != "" {
		out += "\n" + dimStyle.Render("  ↳ mysql: ") + fmt.Sprintf("%s.test:%s", hostname, mysqlPort)
	}
	if pgPort, err := compose.Port(env, "postgres", 5432); err == nil && pgPort != "" {
		out += "\n" + dimStyle.Render("  ↳ postgres: ") + fmt.Sprintf("%s.test:%s", hostname, pgPort)
	}

	return out
}

// scaffoldSubdomains returns the non-empty subdomain prefixes for the project's
// scaffold (used when unregistering proxy routes).
func scaffoldSubdomains(cfg config.ProjectConfig) []string {
	s, err := scaffold.Get(cfg.Scaffold)
	if err != nil {
		return nil
	}
	var out []string
	for subdomain := range s.Subdomains() {
		if subdomain == "" {
			continue
		}
		out = append(out, subdomain)
	}
	return out
}
