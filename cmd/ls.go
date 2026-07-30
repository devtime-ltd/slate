package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/devtime-ltd/slate/internal/compose"
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
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	greenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	redStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
)

func runLs(cmd *cobra.Command, args []string) error {
	// No docker requirement: bare workspaces exist without it, and the
	// compose probe below already degrades to "nothing running".
	running := map[string]bool{}
	if composeOut, err := exec.Command("docker", "compose", "ls", "--format", "json").Output(); err == nil {
		var projects []composeProject
		json.Unmarshal(composeOut, &projects)
		for _, p := range projects {
			running[p.Name] = true
		}
	}

	globalCfg, _ := loadProxyConfig(false)

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

// provisioningRow returns (statusLabel, urlOrHint) when the workspace is
// currently provisioning, has failed, or was created bare, and ("", "")
// otherwise. For failed rows the URL column shows the provision log path,
// for bare rows the up command, so the user has an actionable next step
// inline.
func provisioningRow(wsDir string) (string, string) {
	logHint := dimStyle.Render("log: ") + provisionLogPath(wsDir)
	pid, alive := readProvisioningLock(wsDir)
	if pid > 0 && alive {
		return yellowStyle.Render("provisioning"), logHint
	}
	if pid > 0 && !alive {
		return redStyle.Render("failed"), logHint
	}
	if _, err := os.Stat(filepath.Join(wsDir, ".slate", "provisioning.failed")); err == nil {
		return redStyle.Render("failed"), logHint
	}
	if _, err := os.Stat(unprovisionedMarker(wsDir)); err == nil {
		return yellowStyle.Render("bare"), dimStyle.Render("provision: ") + "slate up " + filepath.Base(wsDir)
	}
	return "", ""
}

// processAlive returns whether a pid is still running. Unix-only (uses signal 0);
// slate targets darwin/linux so no build tag is needed today.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
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
	projects := config.ProjectsByName()
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
	for projectName, projectPath := range projects {
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
			wsName := filepath.Base(path)
			hostname := workspace.HostnameForProject(projectName, wsName)
			isRunning := running["slate__"+hostname]

			wsDir := filepath.Join(projectPath, ".slate", "workspaces", wsName)
			status, url := statusLabel(isRunning), globalCfg.WorkspaceURL(hostname)
			if pStatus, pURL := provisioningRow(wsDir); pStatus != "" {
				status, url = pStatus, pURL
			} else if isRunning {
				if env, err := compose.NewEnv(wsName, wsDir, hostname); err == nil {
					url = workspaceURLBlock(env, hostname, cfg, globalCfg)
				}
			}

			t.Row(projectName, wsName, status, url)
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
	projectName, _ := workspace.ProjectName(cfg.Project)

	var rows [][]string
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path := strings.TrimPrefix(line, "worktree ")
		if path == mainRoot {
			continue
		}
		name := filepath.Base(path)
		hostname := workspace.HostnameForProject(projectName, name)
		isRunning := running["slate__"+hostname]

		wsDir, _ := workspace.WorkspaceDir(name)
		status, url := statusLabel(isRunning), globalCfg.WorkspaceURL(hostname)
		if wsDir != "" {
			if pStatus, pURL := provisioningRow(wsDir); pStatus != "" {
				status, url = pStatus, pURL
			} else if isRunning {
				if env, err := compose.NewEnv(name, wsDir, hostname); err == nil {
					url = workspaceURLBlock(env, hostname, cfg, globalCfg)
				}
			}
		}

		rows = append(rows, []string{name, status, url})
	}
	return rows
}
