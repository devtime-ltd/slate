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

var newBranch string
var newBg bool
var newCd bool
var newAdopt bool

var newCmd = &cobra.Command{
	Use:   "new <name>",
	Short: "Create a new workspace",
	Long:  "Creates a git worktree, generates Docker files, starts containers, installs deps, and registers HTTPS proxy.",
	Args:  cobra.ExactArgs(1),
	RunE:  runNew,
}

func init() {
	newCmd.Flags().StringVarP(&newBranch, "branch", "b", "", "Git branch name (default: slate/<name>)")
	newCmd.Flags().BoolVar(&newBg, "bg", false, "Run container build + lifecycle in the background")
	newCmd.Flags().BoolVar(&newCd, "cd", false, "Spawn a shell in the workspace directory (default from global auto_cd; pass --cd=false to opt out)")
	newCmd.Flags().BoolVar(&newAdopt, "adopt", false, "Carry uncommitted changes from the main checkout into the new worktree")
	newCmd.GroupID = "workspace"
	rootCmd.AddCommand(newCmd)
}

func runNew(cmd *cobra.Command, args []string) error {
	return createWorkspace(args[0], newBranch, newBg, resolveAutoCd(cmd, "cd", newCd), newAdopt)
}

func createWorkspace(name, branch string, bg, cd, adopt bool) error {
	if err := requireDocker(); err != nil {
		return err
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
		return fmt.Errorf("workspace '%s' already exists at %s", name, wsDir)
	}

	wsRoot, err := workspace.WorkspacesRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(wsRoot, 0o755); err != nil {
		return fmt.Errorf("creating workspaces dir: %w", err)
	}

	fmt.Printf("Creating worktree (branch: %s)...\n", branch)
	if err := workspace.CreateWorktree(wsDir, branch); err != nil {
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
	if err := scaffold.Generate(wsDir, mainRoot, cfg); err != nil {
		return fmt.Errorf("generating scaffold: %w", err)
	}
	if s, err := scaffold.Get(cfg.Scaffold); err == nil {
		if err := scaffold.GenerateFileMounts(wsDir, cfg, s); err != nil {
			return fmt.Errorf("generating file mounts: %w", err)
		}
	}
	if err := scaffold.GenerateEnvContainer(wsDir, mainRoot, hostname, projectName, name, cfg, proxyConfig); err != nil {
		return fmt.Errorf("generating .env.container: %w", err)
	}
	if err := scaffold.EnsureGitignore(mainRoot); err != nil {
		fmt.Printf("  warning: could not update .gitignore: %v\n", err)
	}

	opts := provisionOpts{fresh: true, build: true}

	if bg {
		return runBackgroundProvision(name, wsDir, opts, cd)
	}

	env, err := compose.NewEnv(name, wsDir, hostname)
	if err != nil {
		return err
	}
	if err := runWorkspaceLifecycle(env, name, wsDir, hostname, cfg, proxyConfig, opts); err != nil {
		return fmt.Errorf("%w\n\nThe worktree is intact — resume provisioning with:\n  slate up %s", err, name)
	}

	if cd {
		return spawnShellAt(wsDir)
	}
	return nil
}

func hostCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}
