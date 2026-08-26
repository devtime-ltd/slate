package cmd

import (
	"bufio"
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

var newBranch string
var newBase string
var newBg bool
var newCd bool
var newAdopt bool
var newBare bool

var newCmd = &cobra.Command{
	Use:   "new <name>",
	Short: "Create a new workspace",
	Long:  "Creates a git worktree, generates Docker files, starts containers, installs deps, and registers HTTPS proxy.",
	Args:  cobra.ExactArgs(1),
	RunE:  runNew,
}

func init() {
	newCmd.Flags().StringVarP(&newBranch, "branch", "b", "", "Git branch name (default: slate/<name>)")
	newCmd.Flags().StringVar(&newBase, "base", "", "Ref to branch from, e.g. main or origin/main (default: the main checkout's current HEAD)")
	newCmd.Flags().BoolVar(&newBg, "bg", false, "Run container build + lifecycle in the background")
	newCmd.Flags().BoolVar(&newCd, "cd", false, "Spawn a shell in the workspace directory (default from global auto_cd; pass --cd=false to opt out)")
	newCmd.Flags().BoolVar(&newAdopt, "adopt", false, "Carry uncommitted changes from the main checkout into the new worktree")
	newCmd.Flags().BoolVar(&newBare, "bare", false, "Worktree + scaffold only, no containers (provision later with `slate up`)")
	newCmd.MarkFlagsMutuallyExclusive("bare", "bg")
	newCmd.GroupID = "workspace"
	rootCmd.AddCommand(newCmd)
}

func runNew(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := workspace.ValidateName(name); err != nil {
		shorter, rescueErr := offerShorterName(name, err)
		if rescueErr != nil {
			return rescueErr
		}
		name = shorter
	}
	return createWorkspace(name, newBranch, newBase, newBg, resolveAutoCd(cmd, "cd", newCd), newAdopt, newBare)
}

// offerShorterName rescues a too-long `slate new` name with a whole-word
// truncation: prompted at a TTY, appended to the error otherwise. One try only;
// a declined or absent suggestion returns the original validation error.
func offerShorterName(name string, verr error) (string, error) {
	suggestion := workspace.ShortenName(name)
	if suggestion == "" {
		return "", verr
	}
	if !isInteractiveTerminal() {
		return "", fmt.Errorf("%w; try `slate new %s`", verr, suggestion)
	}
	fmt.Printf("Name is too long (%d chars, max %d). Use '%s' instead? [y/N] ", len(name), workspace.MaxNameLen, suggestion)
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		return "", verr
	}
	return suggestion, nil
}

func createWorkspace(name, branch, base string, bg, cd, adopt, bare bool) error {
	// Bare creation touches only git and generated files; it must work
	// without Docker installed.
	if !bare {
		if err := requireDocker(); err != nil {
			return err
		}
	}
	if err := workspace.ValidateName(name); err != nil {
		return err
	}

	if branch == "" {
		branch = "slate/" + name
	}

	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return err
	}

	config.RegisterProject(mainRoot)

	if _, err := os.Stat(filepath.Join(mainRoot, "slate.yml")); err != nil {
		return fmt.Errorf("no slate.yml in this project. Run `slate init <scaffold>` first (e.g. `slate init laravel`)")
	}

	cfg, err := config.LoadProject(mainRoot)
	if err != nil {
		return fmt.Errorf("loading slate.yml: %w", err)
	}

	wsDir, err := workspace.WorkspaceDir(name)
	if err != nil {
		return err
	}

	if _, err := os.Stat(wsDir); err == nil {
		return fmt.Errorf("workspace '%s' already exists at %s\nResume it with `slate up %s`, or free the name with `slate rm %s` first", name, wsDir, name, name)
	}

	wsRoot, err := workspace.WorkspacesRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(wsRoot, 0o755); err != nil {
		return fmt.Errorf("creating workspaces dir: %w", err)
	}

	if base != "" {
		// checked before any worktree mutation so a typo'd ref fails clean
		if err := workspace.VerifyRef(mainRoot, base); err != nil {
			return fmt.Errorf("--base %w", err)
		}
		fmt.Printf("Creating worktree (branch: %s, from %s)...\n", branch, base)
	} else {
		fmt.Printf("Creating worktree (branch: %s)...\n", branch)
	}
	if err := workspace.CreateWorktree(mainRoot, wsDir, branch, base); err != nil {
		return fmt.Errorf("git worktree add failed: %w", err)
	}

	if adopt {
		if changed, err := workspace.AdoptDirtyChanges(mainRoot, wsDir); err != nil {
			fmt.Printf("  warning: could not adopt uncommitted changes: %v\n", err)
		} else if changed {
			fmt.Println("Adopted uncommitted changes from the main checkout (left intact there).")
		} else {
			fmt.Println("No uncommitted changes to adopt.")
		}
	}

	// the worktree may carry its own slate.yml (existing branch, --adopt)
	cfg, err = config.LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		// roll back the just-created worktree so a retry isn't stuck on "already exists"
		workspace.RemoveWorktree(wsDir)
		return err
	}
	warnIfWorkspaceConfigDiffers(mainRoot, wsDir)

	projectName, err := workspace.ProjectName(cfg.Project)
	if err != nil {
		return err
	}
	hostname := workspace.HostnameForProject(projectName, name)

	proxyConfig, err := loadProxyConfig(true)
	if err != nil {
		return err
	}

	fmt.Println("Generating .slate/ and .env.container...")
	id := scaffold.Identity{Project: projectName, Workspace: name, Hostname: hostname}
	if err := scaffold.Generate(wsDir, mainRoot, cfg, id); err != nil {
		return fmt.Errorf("generating scaffold: %w", err)
	}
	if err := scaffold.GenerateEnvContainer(wsDir, mainRoot, hostname, projectName, name, cfg, proxyConfig); err != nil {
		return fmt.Errorf("generating .env.container: %w", err)
	}
	if err := scaffold.EnsureGitignore(mainRoot); err != nil {
		fmt.Printf("  warning: could not update .gitignore: %v\n", err)
	}

	// Bare returns before compose.NewEnv: it must work without Docker or a
	// prior `slate setup` (NewEnv requires the installed entrypoint). File
	// mounts are regenerated by the eventual `slate up`.
	if bare {
		// The marker makes the eventual first `slate up` run the fresh
		// lifecycle: the volumes it creates are brand new. It is required
		// state, not advisory: without it that up would skip initial seeding.
		if err := os.WriteFile(unprovisionedMarker(wsDir), nil, 0o644); err != nil {
			return fmt.Errorf("could not mark the workspace unprovisioned: %w\n\nThe worktree is intact; provision it with `slate up %s --fresh`", err, name)
		}
		fmt.Println()
		fmt.Println(tick() + " " + name + " created (bare: no containers)")
		fmt.Println("Provision with `slate up " + name + "` when needed.")
		if cd {
			return spawnShellAt(wsDir)
		}
		fmt.Printf("Workspace dir: %s\n", wsDir)
		return nil
	}

	env, err := compose.NewEnv(name, wsDir, hostname)
	if err != nil {
		return err
	}
	if s, err := scaffold.Resolve(cfg); err == nil {
		if err := scaffold.GenerateFileMounts(wsDir, cfg, s, appLikeServices(env, cfg)); err != nil {
			return fmt.Errorf("generating file mounts: %w", err)
		}
	}

	opts := provisionOpts{fresh: true, build: true}

	// A configured `new:` hook opts the project into background provisioning
	// so the hook runs right after the fast phase, while containers come up.
	// Same cd/TTY gate as the up hook: scripts and CI get sync provisioning.
	if bg || (cd && cfg.New != "") {
		return runBackgroundProvision(cfg, name, wsDir, opts, cd, cfg.New)
	}

	if err := runWorkspaceLifecycle(env, name, wsDir, hostname, cfg, proxyConfig, opts); err != nil {
		return fmt.Errorf("%w\n\nThe worktree is intact — resume provisioning with:\n  slate up %s", err, name)
	}

	if cd {
		return upAt(cfg, name, wsDir, true)
	}
	return nil
}

func hostCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}
