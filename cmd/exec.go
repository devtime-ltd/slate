package cmd

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/scaffold"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var shellCmd = &cobra.Command{
	Use:   "shell [name]",
	Short: "Bash shell in the app container",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, wsDir, err := resolveNameOrCwd(args)
		if err != nil {
			return err
		}
		if err := awaitProvision(wsDir, 0); err != nil {
			return err
		}
		hostname, err := resolveHostname(name)
		if err != nil {
			return err
		}
		env, err := compose.NewEnv(name, wsDir, hostname)
		if err != nil {
			return err
		}
		return compose.RunInteractive(env, "exec", "app", "bash")
	},
}

var (
	execService      string
	execInteractive  bool
	execPauseWorkers bool
)

var execCmd = &cobra.Command{
	Use:   "exec [flags] -- <command> [args...]",
	Short: "Run an arbitrary command in a workspace container",
	Long: `Run a one-off command inside a workspace's container (default: app).

The workspace is selected like the other tools: -w/--workspace, the
SLATE_WORKSPACE env var, or the current directory.

Runs without a TTY by default so it's safe in scripts and CI; stdin is still
forwarded, so you can pipe input in. Pass -i/--interactive for a TTY-backed
session (REPLs, prompts).

Destructive database commands (migrate:fresh, db:wipe) deadlock against a
live queue worker's polling; --pause-workers stops the running worker
services for the duration and starts them again after.

Examples:
  slate exec -- ./vendor/bin/phpstan analyse
  slate exec -- php artisan migrate --force
  slate exec --pause-workers -- php artisan migrate:fresh --seed
  slate exec -s vite -- npm run build
  slate exec -i -- php artisan tinker`,
	GroupID: "tools",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		wsName, wsDir, err := workspace.ResolveWorkspace()
		if err != nil {
			return err
		}
		if err := awaitProvision(wsDir, 0); err != nil {
			return err
		}
		hostname, err := resolveHostname(wsName)
		if err != nil {
			return err
		}
		env, err := compose.NewEnv(wsName, wsDir, hostname)
		if err != nil {
			return err
		}
		if execPauseWorkers {
			restore, err := pauseWorkers(env, wsDir, execService)
			if err != nil {
				return err
			}
			defer restore()
		}
		if execInteractive {
			runArgs := append([]string{"exec", execService}, args...)
			return compose.RunInteractive(env, runArgs...)
		}
		return compose.ExecPiped(env, execService, args...)
	},
}

var logsCmd = &cobra.Command{
	Use:   "logs [workspace] [service]",
	Short: "Tail logs (default: all services)",
	Args:  cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		var name, wsDir string
		var err error
		var service string

		if workspace.OverrideSet() {
			// -w/SLATE_WORKSPACE already selects the workspace, so a positional
			// is the service name, not the workspace.
			if len(args) > 1 {
				return fmt.Errorf("too many arguments: slate logs [service]")
			}
			if len(args) == 1 {
				service = args[0]
			}
			name, wsDir, err = workspace.ResolveWorkspace()
		} else {
			switch len(args) {
			case 0:
				name, wsDir, err = workspace.ResolveWorkspace()
			case 1:
				name, wsDir, err = resolveNameOrCwd(args[:1])
			case 2:
				name, wsDir, err = resolveNameOrCwd(args[:1])
				service = args[1]
			}
		}
		if err != nil {
			return err
		}

		hostname, err := resolveHostname(name)
		if err != nil {
			return err
		}
		env, err := compose.NewEnv(name, wsDir, hostname)
		if err != nil {
			return err
		}
		if service != "" {
			return compose.Run(env, "logs", "-f", service)
		}
		return compose.Run(env, "logs", "-f")
	},
}

// requireWorkspaceName now accepts 0 or 1 workspace names. When 0 are passed,
// commands using this validator call resolveWorkspaceArg(args) to prompt
// the user with a picker over the project's workspaces.
var requireWorkspaceName cobra.PositionalArgs = func(cmd *cobra.Command, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("too many arguments: slate %s [workspace]", cmd.Name())
	}
	return nil
}

func registerToolCommands() {
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return
	}
	// runs before flag parsing: only SLATE_WORKSPACE/cwd (not -w) can pick the tools' workspace
	wsDir := ""
	if ws := os.Getenv("SLATE_WORKSPACE"); ws != "" {
		if dir, err := workspace.WorkspaceDir(ws); err == nil {
			if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
				wsDir = dir
			}
		}
	} else if _, dir, err := workspace.ResolveFromCwd(); err == nil {
		wsDir = dir
	}
	cfg, err := config.LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		return
	}

	for name, tool := range resolvedTools(cfg) {
		switch t := tool.(type) {
		case config.ExecTool:
			registerExecCommand(name, t)
		case config.DBTool:
			registerDBCommand(name, t)
		}
	}
}

func resolvedTools(cfg config.ProjectConfig) map[string]config.Tool {
	var scaffoldTools map[string]config.Tool
	if s, err := scaffold.Resolve(cfg); err == nil {
		scaffoldTools = s.Tools()
	}
	return cfg.ResolvedTools(scaffoldTools)
}

func execToolSummary(name string, t config.ExecTool) string {
	services := t.Services()
	if len(services) == 1 {
		return "Run " + name + " in the " + services[0] + " container"
	}
	return "Run " + name + " across the " + strings.Join(services[:len(services)-1], ", ") + " and " + services[len(services)-1] + " containers"
}

func registerExecCommand(name string, t config.ExecTool) {
	rootCmd.AddCommand(&cobra.Command{
		Use:                name,
		Short:              execToolSummary(name, t),
		GroupID:            "scaffold",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := t.Validate(); err != nil {
				return fmt.Errorf("tool %s %w", name, err)
			}
			plan, err := t.Plan(args)
			if err != nil {
				return fmt.Errorf("tool %s %w", name, err)
			}
			// Flag parsing is disabled so every arg passes straight through to
			// the tool — including the tool's own -w/--workspace (e.g. npm
			// workspaces). Select a Slate workspace before the tool name
			// (`slate -w api artisan …`) or via SLATE_WORKSPACE; both are
			// applied in PersistentPreRunE.
			wsName, wsDir, err := workspace.ResolveWorkspace()
			if err != nil {
				return err
			}
			if err := awaitProvision(wsDir, 0); err != nil {
				return err
			}
			hostname, err := resolveHostname(wsName)
			if err != nil {
				return err
			}
			env, err := compose.NewEnv(wsName, wsDir, hostname)
			if err != nil {
				return err
			}
			return runExecPlan(plan, cmd.ErrOrStderr(), func(service string, command ...string) error {
				return compose.Exec(env, service, command...)
			})
		},
	})
}

func runExecPlan(plan []config.ExecStep, progress io.Writer, run func(service string, command ...string) error) error {
	if len(plan) == 1 {
		return run(plan[0].Service, plan[0].Command...)
	}
	for i, step := range plan {
		fmt.Fprintln(progress, dimStyle.Render(fmt.Sprintf("[%d/%d] %s: %s", i+1, len(plan), step.Service, strings.Join(step.Command, " "))))
		if err := run(step.Service, step.Command...); err != nil {
			return fmt.Errorf("step %d of %d (%s) failed: %w", i+1, len(plan), step.Service, err)
		}
	}
	return nil
}

func registerDBCommand(name string, t config.DBTool) {
	dbCmd := &cobra.Command{
		Use:     name + " [workspace]",
		Short:   "Connection info for " + t.Scheme + " database",
		GroupID: "scaffold",
		Args:    requireWorkspaceName,
	}
	var openFlag bool
	var urlOnly bool
	dbCmd.Flags().BoolVar(&openFlag, "open", false, "Open connection in default client")
	dbCmd.Flags().BoolVar(&urlOnly, "url", false, "Output only the connection URL (pipeable)")
	dbCmd.RunE = func(cmd *cobra.Command, args []string) error {
		wsName, wsDir, err := resolveNameOrCwd(args)
		if err != nil {
			return err
		}
		if err := awaitProvision(wsDir, 0); err != nil {
			return err
		}
		projectName, err := resolveProject()
		if err != nil {
			return err
		}
		hostname := workspace.HostnameForProject(projectName, wsName)
		env, err := compose.NewEnv(wsName, wsDir, hostname)
		if err != nil {
			return err
		}

		port, err := compose.Port(env, t.Service, t.Port)
		if err != nil {
			return fmt.Errorf("%s service not running", t.Service)
		}

		dbName := scaffold.DeriveDBName(projectName, wsName, "default")
		secretKey, err := config.EnsureSecretKey()
		if err != nil {
			return fmt.Errorf("getting secret key: %w", err)
		}
		password := config.DerivePassword(secretKey, projectName, wsName, t.PasswordSalt)
		connURL := t.Scheme + "://" + t.User + ":" + url.PathEscape(password) + "@127.0.0.1:" + port + "/" + dbName

		if urlOnly {
			fmt.Println(connURL)
			return nil
		}

		fmt.Printf("  Host:     %s.test\n", hostname)
		fmt.Printf("  Port:     %s\n", port)
		fmt.Printf("  User:     %s\n", t.User)
		fmt.Printf("  Password: %s\n", strings.Repeat("•", len(password)))
		fmt.Printf("  Database: %s\n", dbName)
		fmt.Println()
		fmt.Println("  Show password / URL: --url")
		fmt.Println("  Open in client:      --open")

		if openFlag {
			opener := "xdg-open"
			if runtime.GOOS == "darwin" {
				opener = "open"
			}
			hostCommand(opener, connURL).Run()
		}

		return nil
	}
	rootCmd.AddCommand(dbCmd)
}

// holdInterrupts keeps slate alive through a Ctrl-C so its deferred cleanup
// runs; the command under exec still receives it via the shared process group.
func holdInterrupts() (release func()) {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	return func() { signal.Stop(interrupts) }
}

func init() {
	shellCmd.GroupID = "tools"
	logsCmd.GroupID = "tools"

	// Stop flag parsing at the first positional so the target command's own
	// flags (e.g. --memory-limit) pass straight through without a leading --.
	execCmd.Flags().SetInterspersed(false)
	execCmd.Flags().StringVarP(&execService, "service", "s", "app", "Container/service to run the command in")
	execCmd.Flags().BoolVarP(&execInteractive, "interactive", "i", false, "Allocate a TTY (for interactive commands)")
	execCmd.Flags().BoolVar(&execPauseWorkers, "pause-workers", false, "Stop running worker services (e.g. queue) around the command, starting them again after")

	rootCmd.AddCommand(shellCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(execCmd)
}
