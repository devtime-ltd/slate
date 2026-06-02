package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/devtime-ltd/slate/internal/workspace"
)

// resolveWorkspaceArg returns args[0] if present, falls back to the workspace
// containing CWD, and finally prompts the user to pick from the project's
// workspaces.
func resolveWorkspaceArg(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	if name, _, err := workspace.ResolveFromCwd(); err == nil {
		return name, nil
	}
	return pickWorkspace()
}

type workspaceChoice struct {
	name      string
	isRunning bool
}

func pickWorkspace() (string, error) {
	choices, err := listWorkspaceChoices()
	if err != nil {
		return "", err
	}
	if len(choices) == 0 {
		return "", fmt.Errorf("no workspaces in this project. Run `slate new <name>` first")
	}

	fmt.Println("Pick a workspace:")
	width := 0
	for _, c := range choices {
		if len(c.name) > width {
			width = len(c.name)
		}
	}
	for i, c := range choices {
		fmt.Printf("  %d) %-*s  %s\n", i+1, width, c.name, statusLabel(c.isRunning))
	}
	fmt.Printf("Select [1-%d or name]: ", len(choices))

	reader := bufio.NewReader(os.Stdin)
	raw, _ := reader.ReadString('\n')
	answer := strings.TrimSpace(raw)
	if answer == "" {
		return "", fmt.Errorf("no selection")
	}

	if n, err := strconv.Atoi(answer); err == nil {
		if n < 1 || n > len(choices) {
			return "", fmt.Errorf("selection %d out of range", n)
		}
		return choices[n-1].name, nil
	}

	for _, c := range choices {
		if c.name == answer {
			return c.name, nil
		}
	}
	return "", fmt.Errorf("no workspace named %q", answer)
}

func listWorkspaceChoices() ([]workspaceChoice, error) {
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return nil, err
	}

	gitCmd := exec.Command("git", "worktree", "list", "--porcelain")
	gitCmd.Dir = mainRoot
	out, err := gitCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}

	running := runningComposeProjects()
	projectName, _ := workspace.ProjectName("")

	var choices []workspaceChoice
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
		choices = append(choices, workspaceChoice{
			name:      name,
			isRunning: running["slate__"+hostname],
		})
	}
	return choices, nil
}

func runningComposeProjects() map[string]bool {
	running := map[string]bool{}
	out, err := exec.Command("docker", "compose", "ls", "--format", "json").Output()
	if err != nil {
		return running
	}
	var projects []composeProject
	if err := json.Unmarshal(out, &projects); err != nil {
		return running
	}
	for _, p := range projects {
		running[p.Name] = true
	}
	return running
}
