package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	Short: "Claude Code session in the workspace's agent container",
	Long: `Start or continue a Claude Code session inside the workspace's dedicated
agent container: the app image plus the agent toolchain (claude, and Node for
scaffolds whose app image lacks it). Requires "agent: claude" in slate.yml.

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
		cfg, err := config.LoadProjectForWorkspace(mainRoot, wsDir)
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
		return fmt.Errorf("no agent configured; add `agent: claude` to slate.yml, then run `slate up %s --build`", wsName)
	}

	hostname, err := resolveHostname(wsName)
	if err != nil {
		return err
	}
	env, err := compose.NewEnv(wsName, wsDir, hostname)
	if err != nil {
		return err
	}

	if err := compose.ExecQuiet(env, scaffold.AgentService, "sh", "-c", "command -v claude"); err != nil {
		return fmt.Errorf("agent container not available (after enabling `agent: claude` or upgrading slate, the workspace needs a rebuild)\nRebuild with: slate up %s --build", wsName)
	}

	if _, err := os.Stat(filepath.Join(config.AgentClaudeDir(), ".credentials.json")); err != nil {
		fmt.Println("First agent run: complete the login prompt once; it persists across all workspaces.")
	}

	execArgs := []string{
		"exec",
		"-e", "CLAUDE_CONFIG_DIR=" + scaffold.AgentConfigDir,
		"-e", "SLATE_WORKSPACE=" + wsName,
		scaffold.AgentService, "claude",
		"--append-system-prompt", agentBriefing(cfg, wsName, hostname),
	}
	if cfg.ClaudePermissionMode != "" {
		execArgs = append(execArgs, "--permission-mode", cfg.ClaudePermissionMode)
	}
	switch {
	case opts.picker:
		execArgs = append(execArgs, "--resume")
	case !opts.fresh && agentHasSessions(wsDir):
		execArgs = append(execArgs, "--continue")
	default:
		// not on resume: would stomp a manual /rename
		execArgs = append(execArgs, "--name", hostname)
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

// agentBriefing orients the session: what this container can and cannot do,
// and the environment traps agents otherwise rediscover every time.
func agentBriefing(cfg config.ProjectConfig, wsName, hostname string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are in the dedicated agent container of slate workspace %q, served at https://%s.test (browsable from the host, not from here).\n", wsName, hostname)
	b.WriteString(`The project worktree is bind-mounted at /app, shared with the workspace's other containers.
The main repo's .git is mounted read-only: git status/diff/log work, but committing and pushing happen on the host; prepare changes and let the user commit.
You cannot manage sibling containers (no docker access). For service restarts, logs of other services, or reprovisioning, ask the user to run slate on the host: slate restart, slate logs, slate up.
`)
	switch cfg.Scaffold {
	case "laravel":
		b.WriteString(`This container has PHP (composer, artisan, vendor binaries like pest/pint) and Node 22 (npm/npx; node_modules is shared with the vite service).
Sibling services by hostname: mysql:3306 (the workspace's dev database), mailpit:1025 (SMTP), vite:5173.
DB_* in process env point at the dev MySQL, and real env beats phpunit.xml <env> entries unless they set force="true": run tests against sqlite (DB_CONNECTION=sqlite DB_DATABASE=:memory: ./vendor/bin/pest) or a dedicated test database, never the dev one.
`)
	case "nextjs":
		b.WriteString(`This container has Node 22 (npm/npx/yarn) with the project's node_modules.
Sibling services by hostname: postgres:5432 (the workspace's dev database, when enabled), mailpit:1025 (SMTP).
DATABASE_URL in process env points at the dev database: point tests at their own database, never the dev one.
`)
	}
	return b.String()
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
