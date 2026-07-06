package cmd

import (
	"fmt"
	"net/url"
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
	execService     string
	execInteractive bool
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

Examples:
  slate exec -- ./vendor/bin/phpstan analyse
  slate exec -- php artisan migrate --force
  slate exec -s vite -- npm run build
  slate exec -i -- php artisan tinker`,
	GroupID: "tools",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		wsName, wsDir, err := workspace.ResolveWorkspace()
		if err != nil {
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
	cfg, err := config.LoadProject(mainRoot)
	if err != nil {
		return
	}

	s, err := scaffold.Get(cfg.Scaffold)
	if err != nil {
		return
	}

	tools := s.Tools()
	if userTools := cfg.ResolvedTools(); userTools != nil {
		tools = userTools
	}

	for name, tool := range tools {
		switch t := tool.(type) {
		case config.ExecTool:
			registerExecCommand(name, t)
		case config.DBTool:
			registerDBCommand(name, t)
		}
	}
}

func registerExecCommand(name string, t config.ExecTool) {
	rootCmd.AddCommand(&cobra.Command{
		Use:                name,
		Short:              "Run " + name + " in the " + t.Service + " container",
		GroupID:            "scaffold",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flag parsing is disabled so every arg passes straight through to
			// the tool — including the tool's own -w/--workspace (e.g. npm
			// workspaces). Select a Slate workspace before the tool name
			// (`slate -w api artisan …`) or via SLATE_WORKSPACE; both are
			// applied in PersistentPreRunE.
			wsName, wsDir, err := workspace.ResolveWorkspace()
			if err != nil {
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
			fullCmd := append([]string(nil), t.Command...)
			fullCmd = append(fullCmd, args...)
			return compose.Exec(env, t.Service, fullCmd...)
		},
	})
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

func init() {
	shellCmd.GroupID = "tools"
	logsCmd.GroupID = "tools"

	// Stop flag parsing at the first positional so the target command's own
	// flags (e.g. --memory-limit) pass straight through without a leading --.
	execCmd.Flags().SetInterspersed(false)
	execCmd.Flags().StringVarP(&execService, "service", "s", "app", "Container/service to run the command in")
	execCmd.Flags().BoolVarP(&execInteractive, "interactive", "i", false, "Allocate a TTY (for interactive commands)")

	rootCmd.AddCommand(shellCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(execCmd)
}
