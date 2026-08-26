package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/scaffold"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var briefCmd = &cobra.Command{
	Use:     "brief",
	Short:   "Print an agent-facing cheatsheet for driving slate in this project",
	Long:    "Prints a markdown block describing how to drive this project's slate workspaces non-interactively, for pasting into CLAUDE.md or AGENTS.md.",
	GroupID: "tools",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		mainRoot, err := workspace.MainRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadMainProject(mainRoot)
		if err != nil {
			return err
		}
		// The hook runs in the current workspace's worktree when there is
		// one, so cwd-sensitive tools resolve against the workspace; an
		// explicit target that fails must error rather than silently fall
		// back to the main checkout. Resolved before the no-hook return so
		// a worktree-only brief:/agent: still gets its inert-edit note
		// (on stderr, clear of the markdown).
		wsName, dir := "", mainRoot
		if n, d, rerr := workspace.ResolveWorkspace(); rerr == nil {
			wsName, dir = n, d
			warnIfHostExecEditsInert(mainRoot, dir)
		} else if workspace.OverrideSet() {
			return rerr
		}
		fmt.Print(briefText(cfg))
		if cfg.Brief == "" {
			return nil
		}
		project, err := workspace.ProjectName(cfg.Project)
		if err != nil {
			return err
		}
		fmt.Print(briefHookSection(cfg.Brief, project, wsName, dir))
		return nil
	},
}

// briefHookSection runs the `brief:` hook and returns its stdout as a
// markdown section. {{PROJECT}} always expands; {{WORKSPACE}}/{{HOSTNAME}}
// and SLATE_WORKSPACE apply only with a workspace context (wsName set). Any
// failure warns on stderr and omits the section so the brief output stays
// clean, pasteable markdown.
func briefHookSection(command, project, wsName, dir string) string {
	env := []string{"SLATE_PROJECT=" + project}
	if wsName != "" {
		command = expandCommand(command, wsName, project)
		env = append(env, "SLATE_WORKSPACE="+wsName)
	} else {
		command = strings.ReplaceAll(command, "{{PROJECT}}", project)
	}
	res, err := runCapturedHook(command, dir, env, false)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "warning: the brief: hook could not run (%v); omitting project notes\n", err)
		return ""
	case res.timedOut:
		fmt.Fprintf(os.Stderr, "warning: the brief: hook timed out after %s; omitting project notes\n", hookTimeout)
		return ""
	case res.exitCode != 0:
		fmt.Fprintf(os.Stderr, "warning: the brief: hook exited %d; omitting project notes\n", res.exitCode)
		return ""
	}
	text := strings.TrimSpace(res.output)
	if text == "" {
		return ""
	}
	return "\n## Project notes\n\n" + text + "\n"
}

func init() {
	rootCmd.AddCommand(briefCmd)
}

func briefText(cfg config.ProjectConfig) string {
	// Placeholders stay inside backticks throughout: bare <name>/<cmd> reads
	// as an HTML tag to markdown renderers and disappears.
	var b strings.Builder
	b.WriteString("## Slate workspaces\n" +
		"\n" +
		"This project uses slate: each workspace is a git worktree under `.slate/workspaces/<name>` with its own containers, database, and HTTPS URL.\n" +
		"\n" +
		"- Target a workspace non-interactively: set `SLATE_WORKSPACE=<name>` (honoured by every command) or run from inside its worktree. Never rely on interactive pickers or prompts.\n" +
		"- Lifecycle: `slate new <name>` | `slate up` | `slate down` | `slate rm -f <name>` | `slate ls`\n" +
		"- Finishing up: when the user confirms the work is merged, run `slate done` - it verifies the work landed (merged PR or branch ancestry, clean worktree) before tearing anything down. Inside a workspace's own agent session it stages the teardown to fire when the session exits, so make it your last action there; from a session at the main checkout, `slate done <name>` tears the workspace down immediately. If it refuses, report its reasons rather than forcing - `slate rm -f` is the explicit destructive path.\n" +
		"- Run anything in the app container: `slate exec -- <cmd>` (no TTY, stdin forwarded; `-s <service>` for other containers)\n" +
		"- App URL: `https://<project>--<workspace>.test`; logs: `slate logs [service]`\n" +
		"- A workspace may still be provisioning in the background right after creation (`SLATE_PROVISIONING=1` in this session's env means it started that way). `slate exec` and the tool shortcuts wait for it automatically; run `slate wait` to block until it's ready explicitly (instant when already up, non-zero exit + log tail if provisioning failed).\n")

	var toolNames []string
	tools := map[string]config.Tool{}
	if s, err := scaffold.Resolve(cfg); err == nil {
		tools = s.Tools()
	}
	if userTools := cfg.ResolvedTools(); userTools != nil {
		tools = userTools
	}
	for name := range tools {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)
	if len(toolNames) > 0 {
		for i, name := range toolNames {
			toolNames[i] = "`slate " + name + "`"
		}
		fmt.Fprintf(&b, "- Tool shortcuts (run in the right container): %s\n", strings.Join(toolNames, " | "))
	}

	if cfg.Scaffold.Name == "laravel" {
		b.WriteString("- Tests in containers: DB_* are real process env and beat phpunit.xml `<env>` entries unless those set `force=\"true\"`; without that, run tests as `DB_CONNECTION=sqlite DB_DATABASE=:memory: slate exec -- ./vendor/bin/pest` so they never hit the dev database.\n")
	}
	return b.String()
}
