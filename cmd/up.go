package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/proxy"
	"github.com/devtime-ltd/slate/internal/scaffold"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var upBuild bool

var upCmd = &cobra.Command{
	Use:   "up [name]",
	Short: "Start/refresh a workspace (idempotent)",
	Long:  "Brings containers up, refreshes deps, runs migrations. Safe to re-run.",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runUp,
}

func init() {
	upCmd.Flags().BoolVar(&upBuild, "build", false, "Force image rebuild")
	upCmd.GroupID = "workspace"
	rootCmd.AddCommand(upCmd)
}

func runUp(cmd *cobra.Command, args []string) error {
	name, wsDir, err := resolveNameOrCwd(args)
	if err != nil && len(args) > 0 {
		fmt.Printf("Workspace '%s' doesn't exist. Create it? [Y/n] ", args[0])
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer == "" || answer == "y" {
			return runNew(cmd, args)
		}
		return fmt.Errorf("workspace '%s' not found", args[0])
	} else if err != nil {
		return err
	}

	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return err
	}

	cfg, err := config.LoadProject(mainRoot)
	if err != nil {
		return fmt.Errorf("loading slate.yml: %w", err)
	}

	if err := scaffold.Generate(wsDir, cfg); err != nil {
		return fmt.Errorf("generating scaffold: %w", err)
	}
	if s, err := scaffold.Get(cfg.Scaffold); err == nil {
		scaffold.GenerateFileMounts(wsDir, cfg, s)
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

	if err := scaffold.GenerateEnvContainer(wsDir, hostname, projectName, name, cfg, proxyConfig); err != nil {
		return fmt.Errorf("generating .env.container: %w", err)
	}

	env, err := compose.NewEnv(name, wsDir)
	if err != nil {
		return err
	}

	fmt.Printf("Starting containers for %s...\n", hostname)
	upArgs := []string{"up", "-d"}
	if upBuild {
		upArgs = append(upArgs, "--build")
	}
	if err := compose.Run(env, upArgs...); err != nil {
		return fmt.Errorf("compose up failed: %w", err)
	}

	lifecycleScript := scaffold.BuildLifecycleScript(cfg, false)
	if lifecycleScript != "" {
		fmt.Println("Running lifecycle (up)...")
		if err := compose.Exec(env, "app", "sh", "-c", lifecycleScript); err != nil {
			return fmt.Errorf("lifecycle failed: %w", err)
		}
	}

	if err := compose.Run(env, "restart", "queue"); err != nil {
		fmt.Printf("  warning: queue restart failed: %v\n", err)
	}

	services := buildServicePorts(env, cfg)

	if err := proxy.Register(proxyConfig, hostname, services); err != nil {
		return fmt.Errorf("proxy registration failed: %w", err)
	}

	fmt.Printf("✅ %s is up: %s\n", name, proxyConfig.WorkspaceURL(hostname))
	if services["vite"] != "" {
		fmt.Printf("   Vite HMR: %s\n", proxyConfig.ServiceURL("vite", hostname))
	}
	return nil
}

func resolveNameOrCwd(args []string) (string, string, error) {
	if len(args) > 0 {
		name := args[0]
		if err := workspace.ValidateName(name); err != nil {
			return "", "", err
		}
		dir, err := workspace.WorkspaceDir(name)
		if err != nil {
			return "", "", err
		}
		if _, err := os.Stat(dir); err != nil {
			return "", "", fmt.Errorf("workspace '%s' not found", name)
		}
		return name, dir, nil
	}
	return workspace.ResolveFromCwd()
}
