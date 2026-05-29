package workspace

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var nameRegex = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`)
var singleChar = regexp.MustCompile(`^[a-z0-9]$`)
var reserved = map[string]bool{"main": true, "master": true, "default": true, "all": true}

func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("workspace name required")
	}
	if len(name) > 32 {
		return fmt.Errorf("workspace name too long (max 32 chars)")
	}
	if !nameRegex.MatchString(name) && !singleChar.MatchString(name) {
		return fmt.Errorf("name must be lowercase, start with a letter, use only [a-z0-9-], and not end with -")
	}
	if reserved[name] {
		return fmt.Errorf("'%s' is a reserved name", name)
	}
	return nil
}

var mainRootOverride string

func SetMainRootOverride(path string) {
	mainRootOverride = path
}

func MainRoot() (string, error) {
	if mainRootOverride != "" {
		return mainRootOverride, nil
	}
	toplevel, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not inside a git repo")
	}
	// --git-common-dir returns the shared .git path. From a worktree it's
	// typically absolute; from the main checkout it's relative (".git").
	commonDir, err := gitOutputInDir(toplevel, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(toplevel, commonDir)
	}
	return filepath.Dir(commonDir), nil
}

func ProjectName(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	root, err := MainRoot()
	if err != nil {
		return "", err
	}
	return filepath.Base(root), nil
}

func Hostname(name string) (string, error) {
	project, err := ProjectName("")
	if err != nil {
		return "", err
	}
	return HostnameForProject(project, name), nil
}

func HostnameForProject(project, name string) string {
	return project + "--" + name
}


func WorkspacesRoot() (string, error) {
	root, err := MainRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".slate", "workspaces"), nil
}

func WorkspaceDir(name string) (string, error) {
	root, err := WorkspacesRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// ResolveFromCwd returns the workspace name and dir if the CWD is inside
// a slate workspace (under <mainRoot>/.slate/workspaces/<name>). Errors if
// CWD is the main checkout or anywhere else.
func ResolveFromCwd() (string, string, error) {
	toplevel, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("not inside a git worktree")
	}
	wsRoot, err := WorkspacesRoot()
	if err != nil {
		return "", "", err
	}
	if filepath.Dir(toplevel) != wsRoot {
		return "", "", fmt.Errorf("not inside a slate workspace (pass <name> or cd into one)")
	}
	return filepath.Base(toplevel), toplevel, nil
}

func CreateWorktree(dir, branch string) error {
	if out, err := runGit("worktree", "add", dir, "-b", branch); err == nil {
		return nil
	} else if !strings.Contains(out, "already exists") {
		// fresh branch creation failed for a reason other than "branch exists"
		return fmt.Errorf("git worktree add: %s", strings.TrimSpace(out))
	}

	// Branch already exists - check it out into the worktree.
	if out, err := runGit("worktree", "add", dir, branch); err != nil {
		return fmt.Errorf("git worktree add: %s", strings.TrimSpace(out))
	}
	return nil
}

func RemoveWorktree(dir string) error {
	if out, err := runGit("worktree", "remove", "--force", dir); err != nil {
		return fmt.Errorf("git worktree remove: %s", strings.TrimSpace(out))
	}
	return nil
}

func runGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	// When --project targets a project the user isn't currently sitting in,
	// run git against that project's checkout instead of the CWD.
	if mainRootOverride != "" {
		cmd.Dir = mainRootOverride
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}


func gitOutput(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitOutputInDir(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
