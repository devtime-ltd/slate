package cmd

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/safeio"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

var (
	agentNoHold     bool
	agentContinue   bool
	agentForceFresh bool
)

var agentCmd = &cobra.Command{
	Use:   "agent [workspace]",
	Short: "Run the project's agent command in a workspace",
	Long: `Runs the "agent:" command from the main checkout's slate.yml in the
workspace directory. Configure a single command, or a [first-run, thereafter]
pair; the first-run variant is picked on the workspace's first agent entry,
whichever way that entry is reached: slate new records the debt in
.slate/agent-first-run-pending, and the first session that runs settles it
(.slate/agent-started).

A command that returns before it could have hosted a session is treated as a
failed launch rather than a clean exit: slate reports it, leaves a shell in
the workspace, and doesn't record the entry. Pass --no-hold to exit instead.

Arguments after -- are passed through to the agent command (appended to
whichever variant runs), e.g.: slate agent myws -- "review the open PR".
Put {{ARGS}} in the agent: command to control where they land; required
for anything beyond a plain simple command (pipes, redirects, comments).

A workspace still owing its first-run entry but carrying commits or
uncommitted changes made since it was created gets the thereafter variant
first, falling back to first-run if that bails: an agent session slate did
not launch leaves no marker. --continue and --fresh name the variant
outright and never retry the other.

Placeholders expanded: {{WORKSPACE}}, {{PROJECT}}, {{HOSTNAME}}.`,
	GroupID: "tools",
	Args: func(cmd *cobra.Command, args []string) error {
		// only args before -- are positional; everything after passes through
		positional := len(args)
		if dash := cmd.ArgsLenAtDash(); dash >= 0 {
			positional = dash
		}
		if positional > 1 {
			return fmt.Errorf("accepts at most one workspace name before --, got %d", positional)
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		positional, extra := args, []string(nil)
		if dash := cmd.ArgsLenAtDash(); dash >= 0 {
			positional, extra = args[:dash], args[dash:]
		}
		name, wsDir, err := resolveNameOrCwd(positional)
		if err != nil {
			return err
		}
		mainRoot, err := workspace.MainRoot()
		if err != nil {
			return err
		}
		cfg, err := config.LoadProjectForWorkspace(mainRoot, wsDir)
		if err != nil {
			return err
		}
		warnIfWorkspaceConfigDiffers(mainRoot, wsDir)
		if cfg.Agent.IsZero() {
			return holdWorkspaceOpen(wsDir, agentUnconfiguredError(mainRoot, wsDir))
		}
		fresh, forced := false, true
		switch {
		case agentContinue:
		case agentForceFresh:
			fresh = true
		default:
			fresh, forced = agentFresh(mainRoot, wsDir), false
		}
		return runAgent(cfg, name, wsDir, fresh, forced, extra)
	},
}

func agentStartedMarker(wsDir string) string {
	return filepath.Join(wsDir, ".slate", "agent-started")
}

// firstRunPending names the marker for a first-run agent entry the workspace
// still owes.
const firstRunPending = "agent-first-run-pending"

func firstRunPendingMarker(wsDir string) string {
	return filepath.Join(wsDir, ".slate", firstRunPending)
}

// agentFresh decides whether this is the workspace's first agent entry.
// The agent-started marker is the source of truth once it exists: it stops
// a bare workspace's later `slate up` (which sets SLATE_FRESH=1) from
// re-running the first-run variant over a live session. Otherwise the debt
// marker answers it, written by `slate new` and by a failed first-run launch,
// cleared by the first session that runs: the entry point never has to carry
// the signal, so a `slate agent` reached outside the hooks (a tmux session, a
// later shell) still gets the first-run variant it is owed, unless the
// worktree has been worked since (agentWorked). SLATE_FRESH=1 and bareness
// remain as fallbacks for workspaces created before the marker; without any
// of them the entry falls through to the thereafter variant.
func agentFresh(mainRoot, wsDir string) bool {
	// presence is judged through the pinned dir: a symlink planted at a
	// marker's name is not the marker
	dir, err := safeio.OpenDir(filepath.Join(wsDir, ".slate"))
	switch {
	case err == nil:
		defer dir.Close()
		for _, marker := range []string{"agent-started", firstRunPending, "unprovisioned"} {
			// something planted at a marker's name could hide a session
			if safeio.ExistsAt(dir, marker) && !safeio.RegularFileAt(dir, marker) {
				return false
			}
		}
		if safeio.RegularFileAt(dir, "agent-started") {
			return false
		}
		if safeio.RegularFileAt(dir, firstRunPending) {
			// the creation hook's own entry: provisioning may already have
			// written files alongside it, and nothing else has had the chance
			if os.Getenv("SLATE_FRESH") == "1" && readFirstRunDebt(wsDir).Provisioning {
				return true
			}
			return !agentWorked(mainRoot, wsDir)
		}
	case !errors.Is(err, os.ErrNotExist):
		// something planted where .slate should be could hide a session
		return false
	}
	if os.Getenv("SLATE_FRESH") == "1" {
		return !agentWorked(mainRoot, wsDir)
	}
	return err == nil && safeio.RegularFileAt(dir, "unprovisioned")
}

// firstRunDebt is the pending marker's content: the worktree as it stood when
// the debt was recorded, so only work done since reads as a session.
type firstRunDebt struct {
	Head         string `json:"head,omitempty"`
	Tree         string `json:"tree,omitempty"`
	Reflog       int    `json:"reflog,omitempty"`
	Provisioning bool   `json:"provisioning,omitempty"`
	// Corrupt marks a marker that exists but could not be read as a debt: a
	// container-mangled one must not pass for the legacy empty marker.
	Corrupt bool `json:"-"`
}

// recordFirstRunDebt writes the marker with the worktree's current baseline;
// provisioning marks a workspace whose first lifecycle has yet to land, so a
// later refresh may fold that lifecycle's files in whatever the tree shows.
func recordFirstRunDebt(mainRoot, wsDir string, provisioning bool) error {
	debt := firstRunDebt{Provisioning: provisioning}
	// an unregistered worktree has only its container-writable .git to read
	// through, so it gets no baseline and agentWorked fails closed on it
	if gitDir, ok := registeredGitDir(mainRoot, wsDir); ok {
		if head, err := gitFor(gitDir, wsDir, "rev-parse", "HEAD"); err == nil {
			if tree, err := worktreeFingerprint(gitDir, wsDir); err == nil {
				if reflog, err := reflogEntries(gitDir, wsDir); err == nil {
					debt = firstRunDebt{Head: head, Tree: tree, Reflog: reflog, Provisioning: provisioning}
				}
			}
		}
	}
	data, err := json.Marshal(debt)
	if err != nil {
		return err
	}
	return replaceWorkspaceMarker(wsDir, firstRunPending, data)
}

// provisioningBaselineRefresh decides, before a lifecycle runs, whether its
// files may be folded into the baseline afterwards: while the debt is
// outstanding and either its first provisioning has yet to land or nothing has
// worked the tree yet, so out-of-band work already present keeps its evidence.
// Not while a new: hook's session runs alongside the lifecycle.
func provisioningBaselineRefresh(mainRoot, wsDir string) func() {
	if !debtOutstanding(wsDir) {
		return func() {}
	}
	if debt := readFirstRunDebt(wsDir); !debt.Provisioning {
		if agentWorked(mainRoot, wsDir) {
			return func() {}
		}
		// the window opens now, so a lifecycle that fails part way leaves a
		// retry free to fold its files in
		debt.Provisioning = true
		if data, err := json.Marshal(debt); err == nil {
			warnOnMarkerError("a later refresh may fold unrelated changes into the baseline",
				replaceWorkspaceMarker(wsDir, firstRunPending, data))
		}
	}
	return func() {
		if debtOutstanding(wsDir) {
			warnOnMarkerError("provisioning's changes may read as an agent session's work",
				recordFirstRunDebt(mainRoot, wsDir, false))
		}
	}
}

// noteHookOutcome records, for a background provisioner still running, that
// the new: hook returned inside the launch floor and so hosted no session: the
// provisioner may then fold its lifecycle's files into the baseline.
func noteHookOutcome(mainRoot, wsDir string, run hostRun) {
	if !run.bailed() {
		return
	}
	// the marker goes down before the lock is read: a provisioner that has
	// already finished cannot consume it, so this side refreshes instead
	warnOnMarkerError("provisioning's changes may read as an agent session's work",
		writeWorkspaceMarker(wsDir, hookHostedNoSession, nil))
	if _, alive := readProvisioningLock(wsDir); !alive && hookBailed(wsDir) && debtOutstanding(wsDir) {
		warnOnMarkerError("provisioning's changes may read as an agent session's work",
			recordFirstRunDebt(mainRoot, wsDir, false))
	}
}

const hookHostedNoSession = "agent-hook-bailed"

// provisionRefreshWanted is the background provisioner's decision at the end
// of its lifecycle: refresh when launched with no hook alongside, or when the
// hook has since reported it hosted no session.
func provisionRefreshWanted(opts provisionOpts, wsDir string) bool {
	// consumed whatever the launch: a licence left by an earlier provisioning
	// that failed before its second look must not reach this one's
	bailed := hookBailed(wsDir)
	return opts.refreshDebt || bailed
}

// provisionSecondLook runs once the lifecycle's lock has gone, for a hook
// that bailed after the in-lock decision. Nothing else refreshes here: an
// agent may already be at work.
func provisionSecondLook(wsDir string, refresh func()) {
	if hookBailed(wsDir) {
		refresh()
	}
}

// hookBailed consumes the licence a new: hook leaves on returning inside the
// launch floor.
func hookBailed(wsDir string) bool {
	dir, err := safeio.OpenDir(filepath.Join(wsDir, ".slate"))
	if err != nil {
		return false
	}
	defer dir.Close()
	if !safeio.RegularFileAt(dir, hookHostedNoSession) {
		return false
	}
	_ = safeio.RemoveAt(dir, hookHostedNoSession)
	return true
}

// markProvisioned ends the provisioning window a debt records, keeping its
// baseline.
func markProvisioned(wsDir string) {
	debt := readFirstRunDebt(wsDir)
	if !debt.Provisioning || !debtOutstanding(wsDir) {
		return
	}
	debt.Provisioning = false
	if data, err := json.Marshal(debt); err == nil {
		warnOnMarkerError("a later provisioning may fold unrelated changes into the baseline",
			replaceWorkspaceMarker(wsDir, firstRunPending, data))
	}
}

func debtOutstanding(wsDir string) bool {
	dir, err := safeio.OpenDir(filepath.Join(wsDir, ".slate"))
	if err != nil {
		return false
	}
	defer dir.Close()
	return safeio.RegularFileAt(dir, firstRunPending)
}

func readFirstRunDebt(wsDir string) firstRunDebt {
	var debt firstRunDebt
	dir, err := safeio.OpenDir(filepath.Join(wsDir, ".slate"))
	if err != nil {
		return debt
	}
	defer dir.Close()
	if !safeio.RegularFileAt(dir, firstRunPending) {
		return debt
	}
	data, err := safeio.ReadFileAt(dir, firstRunPending, 1<<16)
	if err != nil {
		debt.Corrupt = true
		return debt
	}
	if body := bytes.TrimSpace(data); len(body) > 0 && (body[0] != '{' || json.Unmarshal(body, &debt) != nil) {
		// a recorded debt is a JSON object; "null" decodes cleanly to nothing
		debt = firstRunDebt{Corrupt: true}
	}
	return debt
}

// agentWorked reads git through the main checkout's registration of the
// worktree, never the worktree's own container-writable .git pointer, so an
// unregistered worktree is not judged at all. A marker without a baseline
// (recorded before there was one) falls back to the worktree's reflog and a
// dirty tree.
func agentWorked(mainRoot, wsDir string) bool {
	gitDir, ok := registeredGitDir(mainRoot, wsDir)
	if !ok {
		return true
	}
	head, err := gitFor(gitDir, wsDir, "rev-parse", "HEAD")
	if err != nil {
		// a tree that cannot be judged is not a tree to start over in
		return true
	}
	debt := readFirstRunDebt(wsDir)
	if debt.Corrupt {
		return true
	}
	if debt.Head == "" {
		out, warnings, err := gitForRaw(gitDir, wsDir, "status", "--porcelain", "--untracked-files=normal", "--ignore-submodules=dirty")
		if err != nil || len(statusLines(string(out))) > 0 {
			return true
		}
		if unopened, err := unopenedDirectories(warnings); err != nil || len(unopened) > 0 {
			return true
		}
		moved, err := headMoved(gitDir, wsDir)
		return err != nil || moved
	}
	if head != debt.Head {
		return true
	}
	// a reflog that cannot be read, or has outgrown the cap, cannot clear
	// the tree either; one pruned since the baseline reads as work too, and
	// a baseline taken with no reflog at all (core.logAllRefUpdates off)
	// could not tell a commit reset back from no movement
	if debt.Reflog == 0 {
		return true
	}
	if reflog, err := reflogEntries(gitDir, wsDir); err != nil || reflog != debt.Reflog {
		return true
	}
	tree, err := worktreeFingerprint(gitDir, wsDir)
	return err != nil || tree != debt.Tree
}

// reflogEntries counts the worktree's HEAD reflog, which a session's commit
// grows even when a later reset puts HEAD back where the baseline found it.
func reflogEntries(gitDir, wsDir string) (int, error) {
	entries, err := headReflog(gitDir, wsDir)
	return len(entries), err
}

// headReflog is the worktree's own HEAD reflog, oldest first, as the commits
// each entry moved to. It is read from the registered git dir: git reflog
// show falls through to the branch's log when the worktree has none, and a
// branch's older history is not this worktree's movement.
func headReflog(gitDir, wsDir string) ([]string, error) {
	if gitDir == "" {
		log, err := gitFor(gitDir, wsDir, "reflog", "show", "--format=%H", "HEAD")
		if err != nil || log == "" {
			return nil, err
		}
		entries := strings.Split(log, "\n")
		slices.Reverse(entries)
		return entries, nil
	}
	f, err := os.Open(filepath.Join(gitDir, "logs", "HEAD"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, gitOutputLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > gitOutputLimit {
		return nil, errGitOutputTooLarge
	}
	var commits []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 {
			commits = append(commits, fields[1])
		}
	}
	return commits, nil
}

// worktreeFingerprint hashes the uncommitted changes: every tracked path that
// differs from HEAD and every untracked path, each with its size, mtime and
// first megabyte, so an edit to a path that was already dirty when the
// baseline was taken still changes it and nothing container-sized is ever
// buffered. An entry that cannot be read as a regular file (a symlink, a
// FIFO, a file the host user may not open, a deleted file) contributes its
// path, link target and size/mtime, never a target's contents, and a
// directory git could not open contributes its path and mtime, which moves
// when something is added beneath it.
func worktreeFingerprint(gitDir, wsDir string) (string, error) {
	// "dirty" sees a submodule's commits without git running a status inside
	// it, where a container-writable config could name commands to run
	changed, _, err := gitForRaw(gitDir, wsDir, "diff", "--name-only", "-z", "--ignore-submodules=dirty", "HEAD")
	if err != nil {
		return "", err
	}
	// staging a path already listed as modified changes nothing in the tree,
	// and restaging new content behind restored bytes changes only the blob
	staged, _, err := gitForRaw(gitDir, wsDir, "diff", "--cached", "--raw", "-z", "--ignore-submodules=dirty", "HEAD")
	if err != nil {
		return "", err
	}
	others, warnings, err := gitForRaw(gitDir, wsDir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	dir, err := safeio.OpenDir(wsDir)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	h := sha256.New()
	budget := fingerprintBudget
	for _, rel := range strings.Split(string(changed), "\x00") {
		if rel == "" {
			continue
		}
		if err := hashEntry(h, gitDir, dir, wsDir, rel, &budget); err != nil {
			return "", err
		}
	}
	io.WriteString(h, "\x00\x00staged\x00")
	h.Write(staged)
	io.WriteString(h, "\x00\x00")
	for _, rel := range strings.Split(string(others), "\x00") {
		if rel == "" || slateGenerated(rel) {
			continue
		}
		// an untracked embedded repository is listed as "name/", and is
		// hashed by its own commit like a submodule
		if err := hashEntry(h, gitDir, dir, wsDir, strings.TrimSuffix(rel, "/"), &budget); err != nil {
			return "", err
		}
	}
	unopened, err := unopenedDirectories(warnings)
	if err != nil {
		return "", err
	}
	for _, rel := range unopened {
		fmt.Fprintf(h, "\x00%s\x00unopened", rel)
		if st, err := safeio.StatAt(dir, rel); err == nil {
			fmt.Fprintf(h, " %d", st.Mtim.Nano())
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// unopenedDirectories lists the directories git warned it could not open,
// minus slate's own generated tree. A name holding a newline spreads its
// warning over several lines, past the pattern, so every warning has to be
// accounted for or the listing is not trusted.
func unopenedDirectories(warnings []byte) ([]string, error) {
	matches := unopenedDirectory.FindAllSubmatch(warnings, -1)
	if bytes.Count(warnings, []byte("warning: could not open directory '")) != len(matches) {
		return nil, errUnreadWarning
	}
	var dirs []string
	for _, m := range matches {
		rel := strings.TrimSuffix(string(m[1]), "/")
		if !slateGenerated(rel + "/") {
			dirs = append(dirs, rel)
		}
	}
	return dirs, nil
}

var errUnreadWarning = errors.New("a warning git printed could not be read")

// hashEntry folds one path into the fingerprint through the pinned dir only:
// size, mode, mtime and the first megabyte of a readable regular file, the
// target of a symlink, a submodule's own commit, the metadata of anything
// else, or "missing". A submodule that cannot be read, or content past the
// budget, is an error, so the tree is not judged at all.
func hashEntry(h io.Writer, gitDir string, dir *os.File, wsDir, rel string, budget *int64) error {
	fmt.Fprintf(h, "\x00%s\x00", rel)
	if f, err := safeio.OpenFileAt(dir, rel); err == nil {
		if info, err := f.Stat(); err == nil {
			fmt.Fprintf(h, "%d %o %d ", info.Size(), info.Mode(), info.ModTime().UnixNano())
		}
		// a read that fails part way (the container truncating the file under
		// us) must never look like the baseline
		n, err := io.CopyN(h, f, fingerprintBytes)
		if err != nil && err != io.EOF {
			fmt.Fprintf(h, " read error %d", time.Now().UnixNano())
		}
		f.Close()
		if *budget -= n; *budget < 0 {
			return errFingerprintTooLarge
		}
		return nil
	}
	st, err := safeio.StatAt(dir, rel)
	if err != nil {
		io.WriteString(h, "missing")
		return nil
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		if target, err := safeio.ReadlinkAt(dir, rel); err == nil {
			io.WriteString(h, "-> "+target)
		} else {
			io.WriteString(h, "unreadable link")
		}
	case unix.S_IFDIR:
		// a directory in git's listing is a submodule: its commit is the
		// change, read from its own files without running git in it
		head, err := submoduleHead(gitDir, dir, wsDir, rel)
		if err != nil {
			return fmt.Errorf("submodule %s: %w", rel, err)
		}
		fmt.Fprintf(h, "submodule %s", head)
	default:
		fmt.Fprintf(h, "unreadable %d %o %d", st.Size, st.Mode, st.Mtim.Nano())
	}
	return nil
}

// submoduleHead resolves a submodule's HEAD commit by reading its files: the
// .git pointer under the pinned worktree, then HEAD and its ref (or
// packed-refs) in the git dir it names, which may only lie where git keeps
// submodules and is reached from there through pinned opens. No git runs
// inside the submodule and no path under the worktree is re-walked.
func submoduleHead(gitDir string, dir *os.File, wsDir, rel string) (string, error) {
	var sub *os.File
	if pointer, err := safeio.ReadFileAt(dir, rel+"/.git", 4096); err == nil {
		target := strings.TrimSpace(strings.TrimPrefix(string(pointer), "gitdir:"))
		if !strings.HasPrefix(string(pointer), "gitdir:") || target == "" {
			return "", fmt.Errorf("unrecognised .git pointer")
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(wsDir, rel, target)
		}
		sub, err = submoduleGitDir(gitDir, dir, wsDir, filepath.Clean(target))
		if err != nil {
			return "", err
		}
	} else {
		parent, leaf, done, err := descendDir(dir, rel+"/.git")
		if err != nil {
			return "", err
		}
		defer done()
		sub, err = safeio.OpenDirAt(parent, leaf)
		if err != nil {
			return "", err
		}
	}
	defer sub.Close()
	head, err := safeio.ReadFileAt(sub, "HEAD", 4096)
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(string(head))
	if !strings.HasPrefix(ref, "ref: ") {
		return ref, nil
	}
	ref = strings.TrimPrefix(ref, "ref: ")
	if sha, err := safeio.ReadFileAt(sub, ref, 4096); err == nil {
		return strings.TrimSpace(string(sha)), nil
	}
	packed, err := safeio.ReadFileAt(sub, "packed-refs", gitOutputLimit)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(packed), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[1] == ref {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s not found", ref)
}

// submoduleGitDir opens the git dir a submodule's .git pointer names, a clean
// absolute path. git keeps a linked worktree's submodules under the
// worktree's registered git dir and a main checkout's under its own .git; a
// pointer anywhere else is refused, since the container writes it, and the
// path below the root is descended with pinned opens so no symlink on the
// way is followed.
func submoduleGitDir(gitDir string, dir *os.File, wsDir, target string) (*os.File, error) {
	if gitDir != "" {
		if rel, ok := pathBelow(gitDir, target); ok {
			root, err := safeio.OpenDir(gitDir)
			if err != nil {
				return nil, err
			}
			defer root.Close()
			return openDirBelow(root, rel)
		}
	}
	if rel, ok := pathBelow(filepath.Join(wsDir, ".git"), target); ok {
		return openDirBelow(dir, ".git/"+rel)
	}
	return nil, fmt.Errorf("git dir %s lies outside the worktree's git dir", target)
}

// pathBelow is the slash-separated path of p strictly inside root, comparing
// the paths as given and then with symlinks resolved.
func pathBelow(root, p string) (string, bool) {
	for _, pair := range [][2]string{{root, p}, {resolvePath(root), resolvePath(p)}} {
		prefix := filepath.Clean(pair[0]) + string(filepath.Separator)
		if strings.HasPrefix(pair[1], prefix) {
			return filepath.ToSlash(pair[1][len(prefix):]), true
		}
	}
	return "", false
}

func openDirBelow(root *os.File, rel string) (*os.File, error) {
	parent, leaf, done, err := descendDir(root, rel)
	if err != nil {
		return nil, err
	}
	defer done()
	return safeio.OpenDirAt(parent, leaf)
}

// descendDir pins the parent of rel's leaf under dir, like safeio's reads do.
func descendDir(dir *os.File, rel string) (parent *os.File, leaf string, done func(), err error) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	parent = dir
	for _, name := range parts[:len(parts)-1] {
		next, err := safeio.OpenDirAt(parent, name)
		if parent != dir {
			parent.Close()
		}
		if err != nil {
			return nil, "", nil, err
		}
		parent = next
	}
	return parent, parts[len(parts)-1], func() {
		if parent != dir {
			parent.Close()
		}
	}, nil
}

// fingerprintBytes bounds how much of each file is hashed; the size and
// mtime cover the rest.
const fingerprintBytes = 1 << 20

// fingerprintBudget bounds how much file content one fingerprint reads in
// all, whatever the container has listed; a tree past it is not judged.
var fingerprintBudget int64 = 1 << 30

var errFingerprintTooLarge = errors.New("too much content to fingerprint")

// unopenedDirectory matches git's LC_ALL=C warning for an untracked directory
// it could not read, which it otherwise omits from the listing with exit 0.
// The name is quoted but not escaped, so it runs to the last quote before the
// reason, which carries no colon of its own.
var unopenedDirectory = regexp.MustCompile(`(?m)^warning: could not open directory '(.+)': [^:\n]*$`)

var errNoReflog = errors.New("no HEAD reflog to judge movement by")

// headMoved reports whether the worktree's own HEAD reflog, which starts at
// its checkout, ever pointed anywhere but the checkout commit; a reflog that
// is missing or cannot be read is an error for the caller to fail closed on.
func headMoved(gitDir, wsDir string) (bool, error) {
	entries, err := headReflog(gitDir, wsDir)
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		return false, errNoReflog
	}
	head, err := gitFor(gitDir, wsDir, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	// a commit later reset away left an entry pointing elsewhere
	for _, entry := range entries[1:] {
		if entry != entries[0] {
			return true, nil
		}
	}
	return head != entries[0], nil
}

func init() {
	agentCmd.Flags().BoolVar(&agentNoHold, "no-hold", false, "exit on a failed agent launch instead of leaving a shell in the workspace")
	agentCmd.Flags().BoolVar(&agentContinue, "continue", false, "run the thereafter variant whatever the workspace records, with no first-run retry")
	agentCmd.Flags().BoolVar(&agentForceFresh, "fresh", false, "run the first-run variant whatever the workspace records")
	agentCmd.MarkFlagsMutuallyExclusive("continue", "fresh")
	rootCmd.AddCommand(agentCmd)
}

// agentUnconfiguredError explains a missing `agent:` in terms of where slate
// reads it from. The trap this catches: an `agent:` that lives only in the
// worktree's slate.yml is invisible to slate, so the session never starts.
func agentUnconfiguredError(mainRoot, wsDir string) error {
	mainYml := filepath.Join(mainRoot, "slate.yml")
	if wsCfg, err := config.LoadProject(wsDir); err == nil && !wsCfg.Agent.IsZero() {
		return fmt.Errorf("no `agent:` in %s; this workspace's slate.yml sets one, but host commands only ever come from the main checkout, so land it there", mainYml)
	}
	return fmt.Errorf("no `agent:` in %s; set a command (e.g. `agent: claude`) or a [first-run, thereafter] pair", mainYml)
}

func runAgent(cfg config.ProjectConfig, wsName, wsDir string, fresh, forced bool, extra []string) error {
	command, variant := cfg.Agent.Again, "thereafter"
	if fresh {
		command, variant = cfg.Agent.First, "first-run"
		baselineFirstEntry(wsDir)
	}

	run, err := runHostCommandDetail(cfg, command, wsName, wsDir, fresh, extra, true)
	recordAgentRun(wsDir, variant, run)
	finalFresh := fresh
	if err == nil && run.bailed() {
		err = agentBailedError(run, variant)
		// Bailing without running means the command declined the work it was
		// given, and for the thereafter variant that means it presumed a
		// session the workspace hasn't got (`claude --continue` with nothing
		// to continue exits 0 or 1 depending on the claude build). The
		// first-run variant is what should have run. Signal deaths (-1, >128)
		// are the launch being stopped, not declined: don't start another.
		// 126/127 never reach here (runHostCommandDetail returns them as
		// errors): a command that couldn't run is a config problem to
		// surface, and retrying the other variant would mask it.
		if !forced && run.exitCode >= 0 && run.exitCode <= 128 && !fresh && cfg.Agent.First != "" && cfg.Agent.First != cfg.Agent.Again {
			fmt.Fprintf(os.Stderr, "  %s %v\n", warn(), err)
			fmt.Fprintln(os.Stderr, "  retrying with the first-run variant")
			finalFresh = true
			variant = "first-run"
			baselineFirstEntry(wsDir)
			run, err = runHostCommandDetail(cfg, cfg.Agent.First, wsName, wsDir, true, extra, true)
			recordAgentRun(wsDir, variant, run)
			if err == nil && run.bailed() {
				err = agentBailedError(run, variant)
			}
		}
	}
	if err != nil {
		// Fresh without the marker is reachable two ways (a pre-marker
		// workspace's SLATE_FRESH/bareness, and the thereafter retry), so a
		// failed first-run launch records the debt rather than assuming it.
		// the debt outlives the launch, but the baseline taken before it
		// stays as it was: whatever the launcher wrote before bailing is
		// evidence for the next entry, never a new baseline
		if finalFresh && !debtOutstanding(wsDir) {
			warnOnMarkerError("the workspace will not remember it is owed a first-run entry",
				writeWorkspaceMarker(wsDir, firstRunPending, nil))
		}
		return holdWorkspaceOpen(wsDir, err)
	}
	_ = removeWorkspaceMarker(wsDir, firstRunPending)
	warnOnMarkerError("the next entry will re-run the first-run variant over this workspace",
		writeWorkspaceMarker(wsDir, "agent-started", nil))
	// A session that outlived the launch floor but ended with a plain
	// non-zero exit crashed rather than quit: claude exits 0 from a normal
	// quit, and a signal death (-1, >128) is the user stopping it. Returning
	// would take the enclosing tmux session, and the crash output with it.
	// The agent-started marker stays written: the session existed, so the
	// next entry rightly continues it.
	if run.exitCode > 0 && run.exitCode <= 128 {
		return holdWorkspaceOpen(wsDir, fmt.Errorf("the %s agent session ended with exit %d after %s; holding the workspace so the failure output survives: %s",
			variant, run.exitCode, run.elapsed.Round(time.Second), run.command))
	}
	// The session ended cleanly: run any teardown staged during it (via
	// `slate done`), or offer one when the work provably landed.
	offerTeardownOnExit(wsName, wsDir)
	return nil
}

// baselineFirstEntry gives a first entry with no baseline yet one now, before
// its command can write anything, so a bailed launch's files are evidence.
func baselineFirstEntry(wsDir string) {
	if readFirstRunDebt(wsDir).Head != "" {
		return
	}
	mainRoot, _ := workspace.MainRoot()
	warnOnMarkerError("a bailed launch's files may read as the baseline",
		recordFirstRunDebt(mainRoot, wsDir, false))
}

// warnOnMarkerError surfaces a failed state-bearing marker write: losing one
// silently would misroute the workspace's next variant choice.
func warnOnMarkerError(consequence string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %s could not record agent state (%v); %s\n", warn(), err, consequence)
	}
}

// The workspace marker helpers write into the container-writable .slate dir
// through safeio's pinned-fd primitives, so a concurrent path swap can't
// redirect them to a host file.
func writeWorkspaceMarker(wsDir, name string, data []byte) error {
	dir, err := safeio.OpenDir(filepath.Join(wsDir, ".slate"))
	if err != nil {
		return err
	}
	defer dir.Close()
	return safeio.WriteFileAt(dir, name, data, 0o644)
}

// replaceWorkspaceMarker is writeWorkspaceMarker for a marker other commands
// may be reading at the same time: the debt must never be seen half-written.
func replaceWorkspaceMarker(wsDir, name string, data []byte) error {
	dir, err := safeio.OpenDir(filepath.Join(wsDir, ".slate"))
	if err != nil {
		return err
	}
	defer dir.Close()
	return safeio.ReplaceFileAt(dir, name, data, 0o644)
}

func removeWorkspaceMarker(wsDir, name string) error {
	dir, err := safeio.OpenDir(filepath.Join(wsDir, ".slate"))
	if err != nil {
		return err
	}
	defer dir.Close()
	return safeio.RemoveAt(dir, name)
}

// recordAgentRun drops each agent run's outcome into the workspace. A clean
// quit or a signal death still returns and takes any enclosing tmux session
// with it, so for those shapes this file is the only evidence of what
// happened.
func recordAgentRun(wsDir, variant string, run hostRun) {
	if run.command == "" {
		return
	}
	line := fmt.Sprintf("%s variant=%s exit=%d elapsed=%s command=%s\n",
		time.Now().Format(time.RFC3339), variant, run.exitCode, run.elapsed.Round(time.Millisecond), run.command)
	_ = writeWorkspaceMarker(wsDir, "agent-last-run", []byte(line))
}

func agentBailedError(run hostRun, variant string) error {
	return fmt.Errorf("the %s agent command exited after %s without starting a session (exit %d): %s",
		variant, run.elapsed.Round(time.Millisecond), run.exitCode, run.command)
}

// holdWorkspaceOpen reports a failed agent launch and, at a terminal, leaves a
// shell in the workspace instead of returning. The documented tmux recipe makes
// `slate agent` the session's only command, so returning would tear the session
// down and take the diagnostic with it.
func holdWorkspaceOpen(wsDir string, cause error) error {
	if agentNoHold || !isInteractiveTerminal() {
		return cause
	}
	fmt.Fprintf(os.Stderr, "\n%s %v\n", cross(), cause)
	fmt.Fprintln(os.Stderr, "Holding the workspace open with a shell; fix the command, then run `slate agent` again.")
	return spawnShellAt(wsDir)
}

func expandCommand(command, wsName, project string) string {
	return strings.NewReplacer(
		"{{WORKSPACE}}", wsName,
		"{{PROJECT}}", project,
		"{{HOSTNAME}}", workspace.HostnameForProject(project, wsName),
	).Replace(command)
}

// hostRun is how a slate.yml host command finished. Elapsed time is what
// separates an agent session that ran from a launch that never got off the
// ground: a command can fail on its own terms and still exit 0, which is
// otherwise indistinguishable from a clean quit.
type hostRun struct {
	command  string
	elapsed  time.Duration
	exitCode int
}

func (r hostRun) bailed() bool {
	floor := agentMinRuntime()
	return floor > 0 && r.elapsed < floor
}

const (
	defaultAgentMinRuntime = 3 * time.Second
	maxAgentMinRuntime     = time.Hour
)

// agentMinRuntime is how long an agent command has to survive before slate
// believes a session started. SLATE_AGENT_MIN_RUNTIME (seconds, 0 to disable)
// overrides it, for an `agent:` that legitimately hands off and returns.
// NaN and +Inf parse without error but convert to a 0s or 292-year duration,
// which would silently disable the check or fail every launch, so the range
// is bounded rather than just non-negative.
func agentMinRuntime() time.Duration {
	secs, err := strconv.ParseFloat(os.Getenv("SLATE_AGENT_MIN_RUNTIME"), 64)
	if err != nil || math.IsNaN(secs) || secs < 0 || secs > maxAgentMinRuntime.Seconds() {
		return defaultAgentMinRuntime
	}
	return time.Duration(secs * float64(time.Second))
}

// runHostCommand executes a slate.yml host command via sh -c in the workspace
// dir. Ordinary non-zero exits (ctrl-c, `exit 1`) aren't slate failures, but
// 126 and 127 mean the command itself couldn't run and must be surfaced.
func runHostCommand(cfg config.ProjectConfig, command, wsName, wsDir string, fresh bool) error {
	_, err := runHostCommandDetail(cfg, command, wsName, wsDir, fresh, nil, false)
	return err
}

// agentRun scopes the {{ARGS}}/append processing to `slate agent`: hooks may
// legitimately carry a literal {{ARGS}} for some downstream templater and must
// pass through untouched.
func runHostCommandDetail(cfg config.ProjectConfig, command, wsName, wsDir string, fresh bool, extra []string, agentRun bool) (hostRun, error) {
	project, err := workspace.ProjectName(cfg.Project)
	if err != nil {
		return hostRun{}, err
	}
	expanded := expandCommand(command, wsName, project)
	run := hostRun{command: expanded}
	if agentRun {
		// hooks keep their literal text: execution never rewrites them, so
		// neither may their diagnostics
		run.command = displayCommand(expanded, extra)
	}
	if note := hookNeedsAgentNote(cfg, expanded); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}

	freshEnv := "0"
	if fresh {
		freshEnv = "1"
	}
	provisioningEnv := "0"
	if _, alive := readProvisioningLock(wsDir); alive {
		provisioningEnv = "1"
	}
	// Extra args are single-quote-escaped and spliced in as literal words,
	// never read back from positional parameters: `"$@"` indirection would
	// see whatever `set --`, `shift`, or an enclosing function scope left
	// behind, silently losing the forwarded args. {{ARGS}} in the command
	// pins where they land (expanding to nothing when no args were given),
	// but must sit in plain command text: inside quotes the splice would
	// mangle the quoting, inside a comment it would vanish, and inside a
	// here-document body slate can't judge intent without a real shell
	// lexer, so those placements are refused rather than risking a silent
	// drop. Without the placeholder, args append to the end, which is only
	// offered when the command verifiably has no shell structure at all:
	// with structure present the append can land on the wrong pipeline
	// stage, run as its own command after `&`, or vanish into a trailing
	// comment. Trailing whitespace is trimmed before appending: a block
	// scalar `agent: |` decodes with a trailing newline, which would push
	// the args onto their own shell line.
	const argsPlaceholder = "{{ARGS}}"
	shellArgs := []string{"-c", expanded}
	trimmed := strings.TrimRight(expanded, " \t\r\n")
	switch {
	case !agentRun:
	case strings.Contains(expanded, argsPlaceholder):
		// the exact quoted spellings are a natural way to write it and
		// normalise to the same splice
		script := strings.ReplaceAll(expanded, `'`+argsPlaceholder+`'`, argsPlaceholder)
		script = strings.ReplaceAll(script, `"`+argsPlaceholder+`"`, argsPlaceholder)
		masked := maskQuotedAndComments(script)
		if strings.Count(masked, argsPlaceholder) != strings.Count(script, argsPlaceholder) {
			return hostRun{}, fmt.Errorf(`{{ARGS}} sits where args cannot land (quoted text or a comment); move it into plain command text: %s`, displayCommand(expanded, extra))
		}
		// Each arg is forwarded as its own word, so a placeholder glued to
		// surrounding text (PROMPT={{ARGS}}) would attach only the first arg
		// and set the rest loose as commands of their own.
		if !placeholderStandalone(script) {
			return hostRun{}, fmt.Errorf(`{{ARGS}} must stand alone as its own shell word; each forwarded arg becomes a separate word, so it can't be glued to surrounding text: %s`, displayCommand(expanded, extra))
		}
		if strings.Contains(masked, "<<") {
			return hostRun{}, fmt.Errorf(`{{ARGS}} can't be checked around a here-document; restructure the agent command without <<: %s`, displayCommand(expanded, extra))
		}
		shellArgs = []string{"-c", strings.ReplaceAll(script, argsPlaceholder, shellQuoteAll(extra))}
	case len(extra) == 0:
	// classified on the masked text so metacharacters inside quoted
	// arguments (a prompt mentioning "run()") don't refuse a command whose
	// shell-visible structure is plainly simple
	case strings.ContainsAny(maskQuotedAndComments(trimmed), "|&;()<>#\n") || oddTrailingBackslashes(trimmed):
		// A trailing unquoted backslash would consume the separator and glue
		// the first appended arg onto the command's last word.
		return hostRun{}, fmt.Errorf("the agent command isn't a plain simple command, so appended args could land in the wrong place; put {{ARGS}} where they belong: %s", displayCommand(expanded, extra))
	default:
		shellArgs = []string{"-c", trimmed + " " + shellQuoteAll(extra)}
	}
	c := exec.Command("sh", shellArgs...)
	c.Dir = wsDir
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	agentEnv := "0"
	if agentRun {
		agentEnv = "1"
	}
	c.Env = append(os.Environ(),
		"SLATE_WORKSPACE="+wsName,
		"SLATE_PROJECT="+project,
		"SLATE_FRESH="+freshEnv,
		"SLATE_PROVISIONING="+provisioningEnv,
		"SLATE_AGENT="+agentEnv,
	)

	started := time.Now()
	err = c.Run()
	run.elapsed = time.Since(started)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			run.exitCode = ee.ExitCode()
			if run.exitCode == 126 || run.exitCode == 127 {
				return run, fmt.Errorf("command failed (exit %d, not found/executable?): %s", run.exitCode, run.command)
			}
			return run, nil
		}
		// a start failure has no exit code; without this the breadcrumb
		// would claim exit=0
		run.exitCode = -1
		return run, err
	}
	return run, nil
}

// maskQuotedAndComments blanks quoted spans, escaped characters, and comments
// to spaces, leaving only plain command text, so callers can check what truly
// sits at the shell's top level. The walk follows POSIX rules: '...' spans
// take no escapes, backslash escapes the next character elsewhere, and #
// opens a comment only at the start of a word.
func maskQuotedAndComments(script string) string {
	masked := []byte(script)
	state := byte(0) // 0 plain, '\'' single, '"' double, '#' comment
	for i := 0; i < len(script); i++ {
		c := script[i]
		if state == '#' {
			if c == '\n' {
				state = 0
			} else {
				masked[i] = ' '
			}
			continue
		}
		if state != '\'' && c == '\\' {
			masked[i] = ' '
			if i+1 < len(script) {
				masked[i+1] = ' '
			}
			i++
			continue
		}
		switch {
		case state == 0 && c == '#' && (i == 0 || strings.IndexByte(" \t\n;|&()<>}", script[i-1]) >= 0):
			// the marker survives masking so classifiers can still see a
			// comment exists; only its content is blanked
			state = '#'
		case state == 0 && (c == '\'' || c == '"'):
			state = c
			masked[i] = ' '
		case state == c:
			state = 0
			masked[i] = ' '
		case state != 0:
			masked[i] = ' '
		}
	}
	return string(masked)
}

// placeholderStandalone reports whether every {{ARGS}} occurrence is bounded
// by whitespace, an operator, or the string's ends, i.e. is a whole shell
// word rather than a fragment of one.
func placeholderStandalone(script string) bool {
	const ph = "{{ARGS}}"
	const boundary = " \t\n|;&()<>"
	for i := 0; ; {
		j := strings.Index(script[i:], ph)
		if j < 0 {
			return true
		}
		j += i
		if j > 0 && strings.IndexByte(boundary, script[j-1]) < 0 {
			return false
		}
		i = j + len(ph)
		if i < len(script) && strings.IndexByte(boundary, script[i]) < 0 {
			return false
		}
	}
}

func oddTrailingBackslashes(s string) bool {
	return (len(s)-len(strings.TrimRight(s, `\`)))%2 == 1
}

// shellQuoteAll renders args as single-quoted shell words, the only POSIX
// quoting with no expansions left inside; an embedded single quote is
// closed, backslash-escaped, then reopened (the quote/backslash/quote/quote idiom).
func shellQuoteAll(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// displayCommand is what errors and the agent-last-run breadcrumb show for a
// run: the expanded command with any passed-through args rendered where they
// actually land, at {{ARGS}} when the command places them, appended otherwise,
// shell-quoted the same way execution splices them.
func displayCommand(expanded string, extra []string) string {
	script := strings.ReplaceAll(expanded, `'{{ARGS}}'`, "{{ARGS}}")
	script = strings.ReplaceAll(script, `"{{ARGS}}"`, "{{ARGS}}")
	if strings.Contains(script, "{{ARGS}}") {
		// rendered even with no args, matching execution, which never runs
		// a literal placeholder
		return strings.ReplaceAll(script, "{{ARGS}}", shellQuoteAll(extra))
	}
	if len(extra) == 0 {
		return expanded
	}
	return expanded + " " + shellQuoteAll(extra)
}

// hookNeedsAgentNote catches a `new:`/`up:` hook reaching for an `agent:` that
// isn't configured: the hook's own `slate agent` would fail inside a process
// whose output the hook may never show.
func hookNeedsAgentNote(cfg config.ProjectConfig, expanded string) string {
	if !cfg.Agent.IsZero() || !strings.Contains(expanded, "slate agent") {
		return ""
	}
	return fmt.Sprintf("  %s this hook runs `slate agent`, but the main checkout's slate.yml has no `agent:`; no session will start", warn())
}

// upAt is what new/up drop into after provisioning (behind the
// auto_cd/--cd gate): the up hook if configured, then a shell.
func upAt(cfg config.ProjectConfig, wsName, wsDir string, fresh bool) error {
	if cfg.Up != "" {
		if err := runHostCommand(cfg, cfg.Up, wsName, wsDir, fresh); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: %v\n", err)
		}
	}
	return spawnShellAt(wsDir)
}

// offerTeardownOnExit runs after an agent session ends. It speaks only when
// there is something worth saying: a teardown staged from inside the session
// (via `slate done`), or work that provably landed. Mid-work exits stay
// silent, and nothing is destroyed without a human saying so - except a
// staged teardown, which the human already asked for in-session. A declined
// offer is remembered per tip so re-entering the session doesn't nag.
func offerTeardownOnExit(wsName, wsDir string) {
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return
	}
	ev := checkLanded(mainRoot, wsDir)

	// A staged marker only counts for the tip it was staged at: a marker
	// left behind by an earlier incarnation of this workspace name, or
	// staged before further commits, must not authorise destroying the
	// current state.
	staged, stale := false, false
	if raw, err := os.ReadFile(stagedTeardownMarker(wsDir)); err == nil {
		switch {
		case ev.tip == "":
			// Couldn't determine the current tip (e.g. a transient git error):
			// keep the marker rather than discard a valid staging.
		case strings.TrimSpace(string(raw)) == ev.tip:
			staged = true
		default:
			stale = true
			_ = os.Remove(stagedTeardownMarker(wsDir))
		}
	}

	interactive := isInteractiveTerminal()
	hostname, err := resolveHostname(wsName)
	if err != nil {
		return
	}

	switch {
	case staged && ev.ok:
		fmt.Printf("Teardown was staged in this session (%s).\n", ev.evidence)
		if err := destroyWorkspace(wsName, wsDir, hostname, false, ev.branchOverride(), false, &ev); err != nil {
			fmt.Fprintf(os.Stderr, "warning: staged teardown failed: %v\n", err)
		}
	case staged:
		// The workspace changed between staging and exit; don't destroy
		// work on the strength of a stale request.
		_ = os.Remove(stagedTeardownMarker(wsDir))
		fmt.Printf("Teardown was staged in this session, but %s is no longer safe to remove:\n", wsName)
		for _, r := range ev.reasons {
			fmt.Printf("  - %s\n", r)
		}
		fmt.Println("Staging cleared; run `slate done` again once the work has landed.")
	case stale:
		fmt.Printf("A staged teardown for %s no longer matches the workspace's state; staging cleared - run `slate done` again once ready.\n", wsName)
	case ev.ok && ev.hasWork && interactive:
		if declined, _ := os.ReadFile(teardownDeclinedMarker(wsDir)); strings.TrimSpace(string(declined)) == ev.tip {
			return // already asked about exactly this state; don't nag
		}
		fmt.Printf("Work landed (%s). Tear down %s? [y/N] ", ev.evidence, wsName)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "y" {
			_ = createMarkerFile(teardownDeclinedMarker(wsDir), []byte(ev.tip+"\n"))
			return
		}
		if err := destroyWorkspace(wsName, wsDir, hostname, false, ev.branchOverride(), false, &ev); err != nil {
			fmt.Fprintf(os.Stderr, "warning: teardown failed: %v\n", err)
		}
	}
}
