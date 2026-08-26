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

// MaxNameLen is the longest a workspace name may be. It bounds the derived
// docker/compose identifiers and the *.test hostname.
const MaxNameLen = 32

// ShortenName suggests a valid workspace name at or under MaxNameLen by cutting
// on a word (dash) boundary where possible. It returns "" when the name isn't
// over-long or no valid truncation exists.
func ShortenName(name string) string {
	if len(name) <= MaxNameLen {
		return ""
	}
	cut := name[:MaxNameLen]
	if name[MaxNameLen] != '-' && !strings.HasSuffix(cut, "-") {
		if i := strings.LastIndex(cut, "-"); i >= 0 {
			cut = cut[:i]
		}
	}
	cut = strings.TrimRight(cut, "-")
	if ValidateName(cut) != nil {
		return ""
	}
	return cut
}

func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("workspace name required")
	}
	if len(name) > MaxNameLen {
		return fmt.Errorf("workspace name too long (max %d chars)", MaxNameLen)
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

// CreateWorktree creates dir as a worktree on branch, forking from base when
// given (the main checkout's current HEAD otherwise). Git runs in mainRoot,
// not the CWD: invoked from inside another workspace, a CWD-relative default
// would silently fork from that workspace's HEAD instead.
func CreateWorktree(mainRoot, dir, branch, base string) error {
	// Asked directly rather than inferred from `worktree add -b` failure
	// output, which lumps every "already exists" condition (branch, path)
	// into one string.
	if _, err := runGitIn(mainRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		// The branch already exists - check it out into the worktree. Its
		// history is set, so a requested base can't be honoured and silently
		// ignoring it would be worse than refusing.
		if base != "" {
			return fmt.Errorf("branch '%s' already exists, so it can't be started from '%s'; drop --base to check the branch out as it is", branch, base)
		}
		if out, err := runGitIn(mainRoot, "worktree", "add", dir, branch); err != nil {
			return fmt.Errorf("git worktree add: %s", strings.TrimSpace(out))
		}
		return nil
	}

	args := []string{"worktree", "add", dir, "-b", branch}
	if base != "" {
		args = append(args, base)
	}
	if out, err := runGitIn(mainRoot, args...); err != nil {
		return fmt.Errorf("git worktree add: %s", strings.TrimSpace(out))
	}
	return nil
}

// VerifyRef reports whether ref resolves to a commit in the repo at mainRoot.
func VerifyRef(mainRoot, ref string) error {
	if _, err := runGitIn(mainRoot, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return fmt.Errorf("'%s' does not resolve to a commit (fetch first for remote-only refs?)", ref)
	}
	return nil
}

func RemoveWorktree(dir string) error {
	if out, err := runGit("worktree", "remove", "--force", dir); err != nil {
		return fmt.Errorf("git worktree remove: %s", strings.TrimSpace(out))
	}
	return nil
}

type worktreeEntry struct {
	path   string
	branch string // short name, empty for detached HEAD
}

func parseWorktrees(porcelain string) []worktreeEntry {
	var entries []worktreeEntry
	for _, line := range strings.Split(porcelain, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			entries = append(entries, worktreeEntry{path: path})
		} else if ref, ok := strings.CutPrefix(line, "branch "); ok && len(entries) > 0 {
			entries[len(entries)-1].branch = strings.TrimPrefix(ref, "refs/heads/")
		}
	}
	return entries
}

// CommittedFile reads path from the workspace branch's committed tree via the
// main repository's git dir. The main .git is mounted read-only in containers,
// so unlike the worktree's working files this content is always host-authored.
// ok is false when the branch or path isn't committed (or git fails).
func CommittedFile(mainRoot, wsDir, path string) ([]byte, bool) {
	if mainRoot == "" || wsDir == "" {
		return nil, false
	}
	gitDir := filepath.Join(mainRoot, ".git")
	out, err := exec.Command("git", "--git-dir", gitDir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, false
	}

	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	branch := ""
	target := resolve(wsDir)
	for _, e := range parseWorktrees(string(out)) {
		if e.branch != "" && resolve(e.path) == target {
			branch = e.branch
			break
		}
	}
	if branch == "" {
		return nil, false
	}

	data, err := exec.Command("git", "--git-dir", gitDir, "show", branch+":"+filepath.ToSlash(path)).Output()
	if err != nil {
		return nil, false
	}
	return data, true
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
	for _, e := range parseWorktrees(porcelain) {
		if filepath.Clean(e.path) == target {
			return true
		}
	}
	return false
}

// WorktreeBranch returns the branch checked out in the worktree at dir, or ""
// (detached HEAD, or dir not registered). Stale registrations left by a manual
// `rm -rf` still carry their branch, so this works for those too.
func WorktreeBranch(dir string) string {
	out, err := runGit("worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	target := filepath.Clean(dir)
	for _, e := range parseWorktrees(out) {
		if filepath.Clean(e.path) == target {
			return e.branch
		}
	}
	return ""
}

// CheckedOutBranches returns the branches attached to any registered worktree,
// including the main checkout and stale registrations.
func CheckedOutBranches() map[string]bool {
	out, err := runGit("worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	inUse := map[string]bool{}
	for _, e := range parseWorktrees(out) {
		if e.branch != "" {
			inUse[e.branch] = true
		}
	}
	return inUse
}

// PruneWorktrees drops registrations whose directories no longer exist.
func PruneWorktrees() error {
	if out, err := runGit("worktree", "prune"); err != nil {
		return fmt.Errorf("git worktree prune: %s", strings.TrimSpace(out))
	}
	return nil
}

// BranchesWithPrefix returns local branches whose names start with prefix.
func BranchesWithPrefix(dir, prefix string) ([]string, error) {
	out, err := gitOutputInDir(dir, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+prefix)
	if err != nil {
		return nil, fmt.Errorf("listing branches: %w", err)
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// DefaultBranch returns the repo's default branch (via origin/HEAD, falling
// back to main/master), or "" if none can be determined.
func DefaultBranch(dir string) string {
	if ref, err := gitOutputInDir(dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(ref, "origin/")
	}
	for _, b := range []string{"main", "master"} {
		if _, err := gitOutputInDir(dir, "rev-parse", "--verify", "refs/heads/"+b); err == nil {
			return b
		}
	}
	return ""
}

// BranchSafety reports whether branch can be deleted without losing commits:
// its tip is reachable from its upstream (pushed) or from the default branch
// (merged). The reason is human-readable either way. No network calls; remote
// state is judged from local remote-tracking refs.
func BranchSafety(dir, branch string) (bool, string) {
	sha, err := gitOutputInDir(dir, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return false, "no such branch"
	}

	upstream, _ := gitOutputInDir(dir, "for-each-ref", "--format=%(upstream)", "refs/heads/"+branch)
	upstreamGone := false
	if upstream != "" {
		short := strings.TrimPrefix(upstream, "refs/remotes/")
		if upSha, err := gitOutputInDir(dir, "rev-parse", "--verify", upstream); err != nil {
			upstreamGone = true
		} else if upSha == sha {
			return true, "in sync with " + short
		} else if isAncestor(dir, sha, upSha) {
			return true, "not ahead of " + short
		}
	}

	if def := DefaultBranch(dir); def != "" && def != branch && isAncestor(dir, sha, "refs/heads/"+def) {
		return true, "merged into " + def
	}

	switch {
	case upstreamGone:
		return false, "upstream gone, possibly squash-merged"
	case upstream != "":
		return false, "unpushed commits"
	}
	return false, "never pushed"
}

func isAncestor(dir, commit, ref string) bool {
	_, err := gitOutputInDir(dir, "merge-base", "--is-ancestor", commit, ref)
	return err == nil
}

func DeleteBranch(dir, branch string) error {
	cmd := exec.Command("git", "branch", "-D", branch)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git branch -D: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func runGit(args ...string) (string, error) {
	// When --project targets a project the user isn't currently sitting in,
	// run git against that project's checkout instead of the CWD.
	return runGitIn(mainRootOverride, args...)
}

func runGitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
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
