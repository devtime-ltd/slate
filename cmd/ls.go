package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var lsAll bool

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List workspaces",
	RunE:  runLs,
}

func init() {
	lsCmd.Flags().BoolVarP(&lsAll, "all", "a", false, "List workspaces across all registered projects")
	lsCmd.GroupID = "workspace"
	rootCmd.AddCommand(lsCmd)
}

type composeProject struct {
	Name   string `json:"Name"`
	Status string `json:"Status"`
}

var (
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	greenStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellowStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	borderStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
)

func runLs(cmd *cobra.Command, args []string) error {
	running := map[string]bool{}
	if composeOut, err := exec.Command("docker", "compose", "ls", "--format", "json").Output(); err == nil {
		var projects []composeProject
		json.Unmarshal(composeOut, &projects)
		for _, p := range projects {
			running[p.Name] = true
		}
	}

	httpPort, httpsPort, tls := DetectProxyPorts()
	globalCfg := config.WithPorts(httpPort, httpsPort, tls)

	if lsAll {
		return listAllProjects(running, globalCfg)
	}
	return listCurrentProject(running, globalCfg)
}

func statusLabel(isRunning bool) string {
	if isRunning {
		return greenStyle.Render("running")
	}
	return yellowStyle.Render("stopped")
}

func listCurrentProject(running map[string]bool, globalCfg config.GlobalConfig) error {
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return err
	}
	config.RegisterProject(mainRoot)

	rows := collectWorktrees(mainRoot, running, globalCfg)
	if len(rows) == 0 {
		fmt.Println("No workspaces. Run `slate new <name>` to create one.")
		return nil
	}

	t := table.New().
		Border(lipgloss.RoundedBorder()).BorderStyle(borderStyle).
		Headers("WORKSPACE", "STATUS", "URL").
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1)
			if row == table.HeaderRow {
				return s.Foreground(lipgloss.Color("8")).Bold(true)
			}
			return s
		})

	for _, r := range rows {
		t.Row(r[0], r[1], r[2])
	}

	fmt.Fprintln(os.Stdout, t)
	return nil
}

func listAllProjects(running map[string]bool, globalCfg config.GlobalConfig) error {
	projects := config.ListProjects()
	if len(projects) == 0 {
		fmt.Println("No projects registered. Run `slate init` in a project first.")
		return nil
	}

	t := table.New().
		Border(lipgloss.RoundedBorder()).BorderStyle(borderStyle).
		Headers("PROJECT", "WORKSPACE", "STATUS", "URL").
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1)
			if row == table.HeaderRow {
				return s.Foreground(lipgloss.Color("8")).Bold(true)
			}
			return s
		})

	hasRows := false
	for _, projectPath := range projects {
		projectName := filepath.Base(projectPath)
		gitCmd := exec.Command("git", "worktree", "list", "--porcelain")
		gitCmd.Dir = projectPath
		out, err := gitCmd.Output()
		if err != nil {
			continue
		}

		cfg, _ := config.LoadProject(projectPath)

		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "worktree ") {
				continue
			}
			path := strings.TrimPrefix(line, "worktree ")
			if path == projectPath {
				continue
			}
			wsName := path[strings.LastIndex(path, "/")+1:]
			hostname := workspace.HostnameForProject(projectName, wsName)
			isRunning := running["slate__"+hostname]

			url := globalCfg.WorkspaceURL(hostname)
			if isRunning {
				url += serviceURLs(wsName, hostname, cfg, globalCfg)
			}

			t.Row(projectName, wsName, statusLabel(isRunning), url)
			hasRows = true
		}
	}

	if !hasRows {
		fmt.Println("No workspaces across any registered project.")
		return nil
	}

	fmt.Fprintln(os.Stdout, t)
	return nil
}

func collectWorktrees(mainRoot string, running map[string]bool, globalCfg config.GlobalConfig) [][]string {
	gitCmd := exec.Command("git", "worktree", "list", "--porcelain")
	gitCmd.Dir = mainRoot
	out, err := gitCmd.Output()
	if err != nil {
		return nil
	}

	cfg, _ := config.LoadProject(mainRoot)

	var rows [][]string
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path := strings.TrimPrefix(line, "worktree ")
		if path == mainRoot {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		hostname, _ := workspace.Hostname(name)
		isRunning := running["slate__"+hostname]

		url := globalCfg.WorkspaceURL(hostname)
		if isRunning {
			url += serviceURLs(name, hostname, cfg, globalCfg)
		}

		rows = append(rows, []string{name, statusLabel(isRunning), url})
	}
	return rows
}

func serviceURLs(name, hostname string, cfg config.ProjectConfig, globalCfg config.GlobalConfig) string {
	wsDir, err := workspace.WorkspaceDir(name)
	if err != nil {
		return ""
	}
	env, err := compose.NewEnv(name, wsDir)
	if err != nil {
		return ""
	}

	var lines string

	if vitePort, err := compose.Port(env, "vite", cfg.VitePort); err == nil && vitePort != "" {
		lines += "\n" + dimStyle.Render("  ↳ vite: ") + globalCfg.ServiceURL("vite", hostname)
	}

	if _, err := compose.Port(env, "mailpit", 8025); err == nil {
		lines += "\n" + dimStyle.Render("  ↳ mailpit: ") + globalCfg.ServiceURL("mailpit", hostname)
	}

	if mysqlPort, err := compose.Port(env, "mysql", 3306); err == nil && mysqlPort != "" {
		lines += "\n" + dimStyle.Render("  ↳ mysql: ") + fmt.Sprintf("%s.test:%s", hostname, mysqlPort)
	}

	if pgPort, err := compose.Port(env, "postgres", 5432); err == nil && pgPort != "" {
		lines += "\n" + dimStyle.Render("  ↳ postgres: ") + fmt.Sprintf("%s.test:%s", hostname, pgPort)
	}

	return lines
}
