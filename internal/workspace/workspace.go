package workspace

import (
	"fmt"
	"os/exec"
	"regexp"
	"path/filepath"
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

func MainRoot() (string, error) {
	toplevel, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not inside a git repo")
	}
	commonDir, err := gitOutputInDir(toplevel, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	absCommon, err := filepath.Abs(filepath.Join(toplevel, filepath.Dir(commonDir)))
	if err != nil {
		return "", err
	}
	return absCommon, nil
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

func ResolveFromCwd() (string, string, error) {
	toplevel, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("not inside a git worktree")
	}
	name := filepath.Base(toplevel)
	return name, toplevel, nil
}

func CreateWorktree(dir, branch string) error {
	err := exec.Command("git", "worktree", "add", dir, "-b", branch).Run()
	if err != nil {
		err = exec.Command("git", "worktree", "add", dir, branch).Run()
	}
	return err
}

func RemoveWorktree(dir string) error {
	return exec.Command("git", "worktree", "remove", "--force", dir).Run()
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
