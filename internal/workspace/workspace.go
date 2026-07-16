package workspace

import (
	"bytes"
	"fmt"
	"io"
	"os"
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

// workspaceOverride is set from the --workspace/-w flag or SLATE_WORKSPACE env
// var so commands can target a workspace without relying on the CWD.
var workspaceOverride string

func SetWorkspaceOverride(name string) {
	workspaceOverride = name
}

// OverrideSet reports whether an explicit workspace override (--workspace/-w or
// SLATE_WORKSPACE) is in effect, so callers can fail on a bad explicit target
// instead of falling back to CWD detection or an interactive picker.
func OverrideSet() bool {
	return workspaceOverride != ""
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

// ResolveWorkspace resolves the target workspace from, in order of precedence:
// an explicit override (--workspace/-w flag or SLATE_WORKSPACE env), then the
// CWD. This lets cwd-independent callers (agents, CI) target a workspace.
func ResolveWorkspace() (string, string, error) {
	if workspaceOverride != "" {
		if err := ValidateName(workspaceOverride); err != nil {
			return "", "", err
		}
		dir, err := WorkspaceDir(workspaceOverride)
		if err != nil {
			return "", "", err
		}
		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return "", "", fmt.Errorf("workspace '%s' not found", workspaceOverride)
			}
			return "", "", fmt.Errorf("checking workspace '%s': %w", workspaceOverride, err)
		}
		if !info.IsDir() {
			return "", "", fmt.Errorf("workspace '%s' is not a directory", workspaceOverride)
		}
		return workspaceOverride, dir, nil
	}
	return ResolveFromCwd()
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

// WorktreeRegistered reports whether dir is still registered as a worktree.
// A registration outlives a manual `rm -rf` of the directory.
func WorktreeRegistered(dir string) bool {
	out, err := runGit("worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	return worktreeListed(out, dir)
}

func worktreeListed(porcelain, dir string) bool {
	target := filepath.Clean(dir)
	for _, line := range strings.Split(porcelain, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok && filepath.Clean(path) == target {
			return true
		}
	}
	return false
}

// PruneWorktrees drops registrations whose directories no longer exist.
func PruneWorktrees() error {
	if out, err := runGit("worktree", "prune"); err != nil {
		return fmt.Errorf("git worktree prune: %s", strings.TrimSpace(out))
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

// AdoptDirtyChanges carries uncommitted work from the main checkout into a
// freshly-created worktree: tracked changes (staged + unstaged, relative to
// HEAD) are applied as a patch, and untracked files are copied. The main
// checkout is left untouched. Returns whether anything was adopted.
func AdoptDirtyChanges(mainRoot, wsDir string) (bool, error) {
	adopted := false

	// --binary so a tracked binary change produces an applyable patch rather
	// than a "Binary files differ" stub that `git apply` rejects.
	patch, err := gitRawInDir(mainRoot, "diff", "--binary", "HEAD")
	if err != nil {
		return false, fmt.Errorf("git diff HEAD: %w", err)
	}
	if len(bytes.TrimSpace(patch)) > 0 {
		if err := applyPatchInDir(wsDir, patch); err != nil {
			return false, err
		}
		adopted = true
	}

	others, err := gitOutputInDir(mainRoot, "ls-files", "--others", "--exclude-standard")
	if err == nil && strings.TrimSpace(others) != "" {
		for _, rel := range strings.Split(strings.TrimSpace(others), "\n") {
			// Skip empties and slate's own workspace tree, which shows as
			// untracked on a project's first `slate new` (before .slate/ is
			// gitignored) and would otherwise be copied into itself.
			if rel == "" || rel == ".slate" || strings.HasPrefix(rel, ".slate/") {
				continue
			}
			src := filepath.Join(mainRoot, rel)
			// Regular files only — never follow a symlink, which would copy the
			// target's contents (possibly from outside the repo) into the worktree.
			if info, err := os.Lstat(src); err != nil || !info.Mode().IsRegular() {
				fmt.Fprintf(os.Stderr, "  warning: skipping non-regular untracked file %s\n", rel)
				continue
			}
			if err := copyFile(src, filepath.Join(wsDir, rel)); err != nil {
				return adopted, fmt.Errorf("copying untracked %s: %w", rel, err)
			}
			adopted = true
		}
	}

	return adopted, nil
}

func gitRawInDir(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

func applyPatchInDir(dir string, patch []byte) error {
	cmd := exec.Command("git", "apply", "--")
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(patch)
	var buf bytes.Buffer
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git apply: %s", strings.TrimSpace(buf.String()))
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := in.Stat(); err == nil {
		mode = info.Mode()
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
