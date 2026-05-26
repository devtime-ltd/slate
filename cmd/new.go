package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/proxy"
	"github.com/devtime-ltd/slate/internal/scaffold"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var newBranch string

var newCmd = &cobra.Command{
	Use:   "new <name>",
	Short: "Create a new workspace",
	Long:  "Creates a git worktree, generates Docker files, starts containers, installs deps, and registers HTTPS proxy.",
	Args:  cobra.ExactArgs(1),
	RunE:  runNew,
}

func init() {
	newCmd.Flags().StringVarP(&newBranch, "branch", "b", "", "Git branch name (default: slate/<name>)")
	newCmd.GroupID = "workspace"
	rootCmd.AddCommand(newCmd)
}

func runNew(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := workspace.ValidateName(name); err != nil {
		return err
	}

	branch := newBranch
	if branch == "" {
		branch = "slate/" + name
	}

	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return err
	}

	config.RegisterProject(mainRoot)

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

	wsRoot, _ := workspace.WorkspacesRoot()
	os.MkdirAll(wsRoot, 0o755)

	fmt.Printf("Creating worktree (branch: %s)...\n", branch)
	if err := workspace.CreateWorktree(wsDir, branch); err != nil {
		return fmt.Errorf("git worktree add failed: %w", err)
	}

	projectName, err := workspace.ProjectName(cfg.Project)
	if err != nil {
		return err
	}
	hostname := workspace.HostnameForProject(projectName, name)

	secretKey, _ := config.EnsureSecretKey()
	httpPort, httpsPort, tls := DetectProxyPorts()
	proxyConfig := config.WithPorts(httpPort, httpsPort, tls)
	proxyConfig.SecretKey = secretKey

	fmt.Println("Generating .slate/ and .env.container...")
	if err := scaffold.Generate(wsDir, cfg); err != nil {
		return fmt.Errorf("generating scaffold: %w", err)
	}
	if s, err := scaffold.Get(cfg.Scaffold); err == nil {
		scaffold.GenerateFileMounts(wsDir, cfg, s)
	}
	if err := scaffold.GenerateEnvContainer(wsDir, hostname, projectName, name, cfg, proxyConfig); err != nil {
		return fmt.Errorf("generating .env.container: %w", err)
	}
	scaffold.EnsureGitignore(mainRoot)

	env, err := compose.NewEnv(name, wsDir)
	if err != nil {
		return err
	}

	fmt.Println("Building and starting containers...")
	if err := compose.Run(env, "up", "-d", "--build"); err != nil {
		return fmt.Errorf("compose up failed: %w", err)
	}

	lifecycleScript := scaffold.BuildLifecycleScript(cfg, true)
	if lifecycleScript != "" {
		fmt.Println("Running lifecycle (up + new)...")
		if err := compose.Exec(env, "app", "sh", "-c", lifecycleScript); err != nil {
			return fmt.Errorf("lifecycle failed: %w", err)
		}
	}

	compose.Run(env, "restart", "queue")

	services := buildServicePorts(env, cfg)

	if err := proxy.Register(proxyConfig, hostname, services); err != nil {
		return fmt.Errorf("proxy registration failed: %w", err)
	}

	fmt.Printf("\n✅ Workspace ready: %s\n", proxyConfig.WorkspaceURL(hostname))
	if services["vite"] != "" {
		fmt.Printf("   Vite HMR: %s\n", proxyConfig.ServiceURL("vite", hostname))
	}

	return nil
}

func hostCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}
