package cmd

import (
	"fmt"
	"net/url"
	"runtime"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/scaffold"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var shellCmd = &cobra.Command{
	Use:   "shell [name]",
	Short: "Bash shell in the app container",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, wsDir, err := resolveNameOrCwd(args)
		if err != nil {
			return err
		}
		env, err := compose.NewEnv(name, wsDir)
		if err != nil {
			return err
		}
		return compose.RunInteractive(env, "exec", "app", "bash")
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

		switch len(args) {
		case 0:
			name, wsDir, err = workspace.ResolveFromCwd()
		case 1:
			name, wsDir, err = resolveNameOrCwd(args[:1])
		case 2:
			name, wsDir, err = resolveNameOrCwd(args[:1])
			service = args[1]
		}
		if err != nil {
			return err
		}

		env, err := compose.NewEnv(name, wsDir)
		if err != nil {
			return err
		}
		if service != "" {
			return compose.Run(env, "logs", "-f", service)
		}
		return compose.Run(env, "logs", "-f")
	},
}


var requireWorkspaceName cobra.PositionalArgs = func(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("workspace name required: slate %s <workspace>", cmd.Name())
	}
	if len(args) > 1 {
		return fmt.Errorf("too many arguments: slate %s <workspace>", cmd.Name())
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
			wsName, wsDir, err := workspace.ResolveFromCwd()
			if err != nil {
				return err
			}
			env, err := compose.NewEnv(wsName, wsDir)
			if err != nil {
				return err
			}
			fullCmd := make([]string, len(t.Command))
			copy(fullCmd, t.Command)
			fullCmd = append(fullCmd, args...)
			return compose.Exec(env, t.Service, fullCmd...)
		},
	})
}

func registerDBCommand(name string, t config.DBTool) {
	dbCmd := &cobra.Command{
		Use:     name + " <workspace>",
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
		env, err := compose.NewEnv(wsName, wsDir)
		if err != nil {
			return err
		}

		port, err := compose.Port(env, t.Service, t.Port)
		if err != nil {
			return fmt.Errorf("%s service not running", t.Service)
		}

		projectName, _ := workspace.ProjectName("")
		dbName := scaffold.DeriveDBName(projectName, wsName, "default")
		secretKey, _ := config.EnsureSecretKey()
		password := config.DerivePassword(secretKey, projectName, wsName, t.PasswordSalt)
		connURL := t.Scheme + "://" + t.User + ":" + url.PathEscape(password) + "@127.0.0.1:" + port + "/" + dbName

		if urlOnly {
			fmt.Println(connURL)
			return nil
		}

		hostname, _ := workspace.Hostname(wsName)
		fmt.Printf("  Host:     %s.test\n", hostname)
		fmt.Printf("  Port:     %s\n", port)
		fmt.Printf("  User:     %s\n", t.User)
		fmt.Printf("  Password: %s\n", password)
		fmt.Printf("  Database: %s\n", dbName)
		fmt.Printf("  URL:      %s\n", connURL)

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
	rootCmd.AddCommand(shellCmd)
	rootCmd.AddCommand(logsCmd)
}
