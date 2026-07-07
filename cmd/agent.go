package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/scaffold"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	agentNew    bool
	agentResume bool
)

var agentCmd = &cobra.Command{
	Use:   "agent [workspace]",
	Short: "Claude Code session in the app container",
	Long: `Start or continue a Claude Code session inside the workspace's app
container. Requires "agent: claude" in slate.yml (the image installs the
claude CLI at build time).

Credentials and settings are shared across workspaces (one login per slate
installation); session history is stored per workspace, so continuing a
session here can only ever pick up this workspace's conversations.

By default the latest session for the workspace is continued, or a fresh one
started if there is none. Use --new to force a fresh session, --resume to
pick from the workspace's past sessions.`,
	GroupID: "tools",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, wsDir, err := resolveNameOrCwd(args)
		if err != nil {
			return err
		}
		mainRoot, err := workspace.MainRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadProject(mainRoot)
		if err != nil {
			return err
		}
		warnIfWorkspaceConfigDiffers(mainRoot, wsDir)
		return runAgentSession(cfg, name, wsDir, agentOpts{fresh: agentNew, picker: agentResume})
	},
}

func init() {
	agentCmd.Flags().BoolVar(&agentNew, "new", false, "Start a fresh session instead of continuing the latest")
	agentCmd.Flags().BoolVarP(&agentResume, "resume", "r", false, "Pick a past session of this workspace to resume")
	rootCmd.AddCommand(agentCmd)
}

type agentOpts struct {
	fresh  bool
	picker bool
}

func runAgentSession(cfg config.ProjectConfig, wsName, wsDir string, opts agentOpts) error {
	if !cfg.AgentEnabled() {
		target := "slate.yml in the main checkout (not the workspace's copy)"
		if mainRoot, err := workspace.MainRoot(); err == nil {
			target = filepath.Join(mainRoot, "slate.yml")
		}
		return fmt.Errorf("no agent configured for this project; add `agent: claude` to %s, then run `slate up %s --build`", target, wsName)
	}

	hostname, err := resolveHostname(wsName)
	if err != nil {
		return err
	}
	env, err := compose.NewEnv(wsName, wsDir, hostname)
	if err != nil {
		return err
	}

	service := agentService(cfg)
	if err := compose.ExecQuiet(env, service, "sh", "-c", "command -v claude"); err != nil {
		return fmt.Errorf("claude not found in the %s container (is the workspace up, and was the image built with `agent: claude` set?)\nRebuild with: slate up %s --build", service, wsName)
	}

	if _, err := os.Stat(filepath.Join(config.AgentClaudeDir(), ".credentials.json")); err != nil {
		fmt.Println("First agent run: complete the login prompt once; it persists across all workspaces.")
	}

	execArgs := []string{
		"exec",
		"-e", "CLAUDE_CONFIG_DIR=" + scaffold.AgentConfigDir,
		"-e", "SLATE_WORKSPACE=" + wsName,
		service, "claude",
	}
	switch {
	case opts.picker:
		execArgs = append(execArgs, "--resume")
	case !opts.fresh && agentHasSessions(wsDir):
		execArgs = append(execArgs, "--continue")
	}

	// as with spawnShellAt, a non-zero session exit isn't a slate failure
	if err := compose.RunInteractive(env, execArgs...); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil
		}
		return err
	}
	return nil
}

func agentService(cfg config.ProjectConfig) string {
	if s, err := scaffold.Get(cfg.Scaffold); err == nil {
		if services := s.AppLikeServices(); len(services) > 0 {
			return services[0]
		}
	}
	return "app"
}

// agentHasSessions reports whether the workspace has recorded sessions
// (claude errors on --continue when there is no history).
func agentHasSessions(wsDir string) bool {
	matches, _ := filepath.Glob(filepath.Join(wsDir, ".slate", "agent", "projects", "*", "*.jsonl"))
	return len(matches) > 0
}

// landAt dispatches the resolved `landing` after new/up (behind the
// auto_cd/--cd gate).
func landAt(cfg config.ProjectConfig, wsName, wsDir string) error {
	switch cfg.ResolvedLanding() {
	case "none":
		return nil
	case "agent":
		return runAgentSession(cfg, wsName, wsDir, agentOpts{})
	case "agent+shell":
		if err := runAgentSession(cfg, wsName, wsDir, agentOpts{}); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: agent session failed: %v\n", err)
		}
		return spawnShellAt(wsDir)
	default:
		return spawnShellAt(wsDir)
	}
}
