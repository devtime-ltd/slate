package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
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

// wsPorts holds one compose project's published ports: service ->
// container port -> host port.
type wsPorts map[string]map[int]string

// runningComposePorts maps compose project name -> published ports for all
// running containers, from a single `docker ps` call (the per-service
// `docker compose port` probes this replaces dominated ls runtime).
func runningComposePorts() map[string]wsPorts {
	out, err := exec.Command("docker", "ps", "--format", "json", "--filter", "label=com.docker.compose.project").Output()
	if err != nil {
		return map[string]wsPorts{}
	}

	res := map[string]wsPorts{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var c struct {
			Labels string `json:"Labels"`
			Ports  string `json:"Ports"`
		}
		if json.Unmarshal([]byte(line), &c) != nil {
			continue
		}
		var project, service string
		for _, kv := range strings.Split(c.Labels, ",") {
			if v, ok := strings.CutPrefix(kv, "com.docker.compose.project="); ok {
				project = v
			}
			if v, ok := strings.CutPrefix(kv, "com.docker.compose.service="); ok {
				service = v
			}
		}
		if project == "" || service == "" {
			continue
		}
		if res[project] == nil {
			res[project] = wsPorts{}
		}
		if res[project][service] == nil {
			res[project][service] = map[int]string{}
		}
		// "0.0.0.0:32992->3306/tcp, [::]:32992->3306/tcp"; unpublished
		// entries ("3306/tcp") have no arrow and are skipped.
		for _, m := range strings.Split(c.Ports, ", ") {
			if strings.Contains(m, "[::]") {
				continue
			}
			host, rest, ok := strings.Cut(m, "->")
			if !ok {
				continue
			}
			portStr, _, _ := strings.Cut(rest, "/")
			containerPort, err := strconv.Atoi(portStr)
			if err != nil {
				continue
			}
			if i := strings.LastIndex(host, ":"); i >= 0 {
				res[project][service][containerPort] = host[i+1:]
			}
		}
	}
	return res
}

// portsLookup adapts a wsPorts map to the portFor shape workspaceURLs
// takes. Safe on nil maps.
func portsLookup(p wsPorts) func(string, int) string {
	return func(service string, containerPort int) string {
		return p[service][containerPort]
	}
}

var (
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	greenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	redStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	boldStyle   = lipgloss.NewStyle().Bold(true)
	urlStyle    = lipgloss.NewStyle().Underline(true)
	fadedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("248"))
	prStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	mergedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("98")).Strikethrough(true)
)

const statusOrb = "⏺"

func runLs(cmd *cobra.Command, args []string) error {
	// No docker requirement: bare workspaces exist without it, and the
	// probe degrades to "nothing running".
	running := runningComposePorts()

	globalCfg, _ := loadProxyConfig(false)

	if lsAll {
		return listAllProjects(running, globalCfg)
	}
	return listCurrentProject(running, globalCfg)
}

func workspaceState(isRunning bool, hostname string, globalCfg config.GlobalConfig) (string, string, string) {
	if isRunning {
		return greenStyle.Render(statusOrb), "", urlStyle.Render(globalCfg.WorkspaceURL(hostname))
	}
	return dimStyle.Render(statusOrb), dimStyle.Render("stopped"), ""
}

// provisioningRow returns (orb, status, hint) for provisioning/failed/bare
// workspaces, empties otherwise. Hints carry the next step inline: the log
// path for failed, the up command for bare.
func provisioningRow(wsDir string) (string, string, string) {
	logHint := dimStyle.Render("log: ") + provisionLogPath(wsDir)
	pid, alive := readProvisioningLock(wsDir)
	if pid > 0 && alive {
		return yellowStyle.Render(statusOrb), yellowStyle.Render("provisioning"), logHint
	}
	if pid > 0 && !alive {
		return redStyle.Render(statusOrb), redStyle.Render("failed"), logHint
	}
	if _, err := os.Stat(filepath.Join(wsDir, ".slate", "provisioning.failed")); err == nil {
		return redStyle.Render(statusOrb), redStyle.Render("failed"), logHint
	}
	if _, err := os.Stat(unprovisionedMarker(wsDir)); err == nil {
		return yellowStyle.Render(statusOrb), yellowStyle.Render("bare"), dimStyle.Render("provision: ") + "slate up " + filepath.Base(wsDir)
	}
	return "", "", ""
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

type wsRow struct {
	name    string
	path    string
	branch  string
	orb     string
	status  string
	url     string
	details string
	markers string
}

// gitMarkers summarises a worktree's git state: "*" for uncommitted changes
// (including untracked), "↑n"/"↓n" for commits ahead/behind the upstream.
// With no upstream set, ahead is counted against origin/HEAD instead so
// never-pushed branches still surface.
func gitMarkers(worktree string) string {
	out, err := exec.Command("git", "-C", worktree, "status", "--porcelain=v2", "--branch").Output()
	if err != nil {
		return ""
	}
	dirty, ahead, behind, hasUpstream := false, 0, 0, false
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ab, ok := strings.CutPrefix(line, "# branch.ab "); ok {
			hasUpstream = true
			fmt.Sscanf(ab, "+%d -%d", &ahead, &behind)
		} else if line != "" && !strings.HasPrefix(line, "# ") {
			// headers always precede entries, so branch.ab is already seen
			dirty = true
			break
		}
	}
	if !hasUpstream {
		if countOut, err := exec.Command("git", "-C", worktree, "rev-list", "--count", "origin/HEAD..HEAD").Output(); err == nil {
			ahead, _ = strconv.Atoi(strings.TrimSpace(string(countOut)))
		}
	}

	m := ""
	if dirty {
		m += "*"
	}
	if ahead > 0 {
		m += fmt.Sprintf("↑%d", ahead)
	}
	if behind > 0 {
		m += fmt.Sprintf("↓%d", behind)
	}
	if m == "" {
		return ""
	}
	return yellowStyle.Render(m)
}

func fillGitMarkers(rowSets ...[]wsRow) {
	var wg sync.WaitGroup
	for _, rows := range rowSets {
		for i := range rows {
			wg.Add(1)
			go func(r *wsRow) {
				defer wg.Done()
				r.markers = gitMarkers(r.path)
			}(&rows[i])
		}
	}
	wg.Wait()
}

type worktreeEntry struct {
	path   string
	branch string
}

func parseWorktrees(out string) []worktreeEntry {
	var entries []worktreeEntry
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		var e worktreeEntry
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "worktree ") {
				e.path = strings.TrimPrefix(line, "worktree ")
			} else if strings.HasPrefix(line, "branch refs/heads/") {
				e.branch = strings.TrimPrefix(line, "branch refs/heads/")
			}
		}
		if e.path != "" {
			entries = append(entries, e)
		}
	}
	return entries
}

type prRef struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	State       string `json:"state"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
}

// prsByBranch maps head branch -> open or merged PR via one `gh pr list`
// call. Closed-unmerged PRs are skipped, and an open PR wins over a merged
// one on the same branch. Any failure (gh missing, no remote, offline)
// returns nil and the listing just omits PR numbers.
func prsByBranch(dir string) map[string]prRef {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--state", "all", "--json", "number,headRefName,headRefOid,url,state", "--limit", "200")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var prs []prRef
	if json.Unmarshal(out, &prs) != nil {
		return nil
	}
	m := make(map[string]prRef, len(prs))
	for _, pr := range prs {
		if pr.State == "CLOSED" {
			continue
		}
		if cur, ok := m[pr.HeadRefName]; ok && (cur.State == "OPEN" || cur.State == pr.State) {
			continue
		}
		m[pr.HeadRefName] = pr
	}
	return m
}

// hyperlink wraps text in an OSC 8 escape so supporting terminals make it
// clickable; others ignore the sequence and show the text unchanged.
func hyperlink(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

func printWorkspaceBlock(indent string, r wsRow, prs map[string]prRef) {
	header := indent + r.orb + " " + boldStyle.Render(r.name)
	if r.status != "" {
		header += dimStyle.Render(" · ") + r.status
	}
	if r.url != "" {
		header += ": " + r.url
	}
	trailer := r.markers
	if pr, ok := prs[r.branch]; ok {
		style := prStyle
		if pr.State == "MERGED" {
			style = mergedStyle
		}
		badge := style.Render(fmt.Sprintf("#%d", pr.Number))
		if pr.URL != "" {
			badge = hyperlink(pr.URL, badge)
		}
		if trailer != "" {
			trailer += " "
		}
		trailer += badge
	}
	if trailer != "" {
		header += dimStyle.Render(" · ") + trailer
	}
	fmt.Println(header)
	for _, line := range strings.Split(r.details, "\n") {
		if line != "" {
			fmt.Println(indent + "  " + line)
		}
	}
}

// Blank lines separate blocks, except consecutive single-line rows, which
// pack together.
func printWorkspaceBlocks(indent string, rows []wsRow, prs map[string]prRef) {
	prevHadDetails := false
	for i, r := range rows {
		if i > 0 && (prevHadDetails || r.details != "") {
			fmt.Println()
		}
		printWorkspaceBlock(indent, r, prs)
		prevHadDetails = r.details != ""
	}
}

func listCurrentProject(running map[string]wsPorts, globalCfg config.GlobalConfig) error {
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return err
	}
	config.RegisterProject(mainRoot)

	prCh := make(chan map[string]prRef, 1)
	go func() { prCh <- prsByBranch(mainRoot) }()

	rows := collectWorktrees(mainRoot, running, globalCfg)
	if len(rows) == 0 {
		fmt.Println("No workspaces. Run `slate new <name>` to create one.")
		return nil
	}

	fillGitMarkers(rows)
	prs := <-prCh
	printWorkspaceBlocks("", rows, prs)
	return nil
}

func listAllProjects(running map[string]wsPorts, globalCfg config.GlobalConfig) error {
	projects := config.ProjectsByName()
	if len(projects) == 0 {
		fmt.Println("No projects registered. Run `slate init` in a project first.")
		return nil
	}

	names := make([]string, 0, len(projects))
	for name := range projects {
		names = append(names, name)
	}
	sort.Strings(names)

	prByProject := make(map[string]map[string]prRef, len(names))
	var prMu sync.Mutex
	var prWg sync.WaitGroup
	for _, projectName := range names {
		prWg.Add(1)
		go func(name, path string) {
			defer prWg.Done()
			m := prsByBranch(path)
			prMu.Lock()
			prByProject[name] = m
			prMu.Unlock()
		}(projectName, projects[projectName])
	}

	type projectListing struct {
		name string
		rows []wsRow
	}
	var listings []projectListing
	for _, projectName := range names {
		projectPath := projects[projectName]
		gitCmd := exec.Command("git", "worktree", "list", "--porcelain")
		gitCmd.Dir = projectPath
		out, err := gitCmd.Output()
		if err != nil {
			continue
		}

		cfg, _ := config.LoadProject(projectPath)

		var rows []wsRow
		for _, wt := range parseWorktrees(string(out)) {
			if wt.path == projectPath {
				continue
			}
			wsName := filepath.Base(wt.path)
			hostname := workspace.HostnameForProject(projectName, wsName)
			ports := running["slate__"+hostname]
			isRunning := len(ports) > 0

			wsDir := filepath.Join(projectPath, ".slate", "workspaces", wsName)
			orb, status, url := workspaceState(isRunning, hostname, globalCfg)
			details := ""
			if pOrb, pStatus, pHint := provisioningRow(wsDir); pOrb != "" {
				orb, status, url, details = pOrb, pStatus, "", pHint
			} else if isRunning {
				_, subs := workspaceURLs(portsLookup(ports), hostname, cfg, globalCfg)
				details = strings.Join(subs, "\n")
			}

			rows = append(rows, wsRow{name: wsName, path: wt.path, branch: wt.branch, orb: orb, status: status, url: url, details: details})
		}
		if len(rows) > 0 {
			listings = append(listings, projectListing{name: projectName, rows: rows})
		}
	}

	if len(listings) == 0 {
		fmt.Println("No workspaces across any registered project.")
		return nil
	}

	rowSets := make([][]wsRow, len(listings))
	for i, l := range listings {
		rowSets[i] = l.rows
	}
	fillGitMarkers(rowSets...)
	prWg.Wait()
	for i, l := range listings {
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(projectRule(l.name))
		fmt.Println(dimStyle.Render("  " + projects[l.name]))
		fmt.Println()
		printWorkspaceBlocks("", l.rows, prByProject[l.name])
	}
	return nil
}

// projectRule renders "── name ──────" at terminal width, capped at 72.
func projectRule(name string) string {
	width := 72
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 && w < width {
		width = w
	}
	tail := width - lipgloss.Width("─ "+name+" ")
	if tail < 4 {
		tail = 4
	}
	return dimStyle.Render("─ ") + boldStyle.Render(name) + " " + dimStyle.Render(strings.Repeat("─", tail))
}

func collectWorktrees(mainRoot string, running map[string]wsPorts, globalCfg config.GlobalConfig) []wsRow {
	gitCmd := exec.Command("git", "worktree", "list", "--porcelain")
	gitCmd.Dir = mainRoot
	out, err := gitCmd.Output()
	if err != nil {
		return nil
	}

	cfg, _ := config.LoadProject(mainRoot)
	projectName, _ := workspace.ProjectName(cfg.Project)

	var rows []wsRow
	for _, wt := range parseWorktrees(string(out)) {
		if wt.path == mainRoot {
			continue
		}
		name := filepath.Base(wt.path)
		hostname := workspace.HostnameForProject(projectName, name)
		ports := running["slate__"+hostname]
		isRunning := len(ports) > 0

		wsDir, _ := workspace.WorkspaceDir(name)
		orb, status, url := workspaceState(isRunning, hostname, globalCfg)
		details := ""
		if wsDir != "" {
			if pOrb, pStatus, pHint := provisioningRow(wsDir); pOrb != "" {
				orb, status, url, details = pOrb, pStatus, "", pHint
			} else if isRunning {
				_, subs := workspaceURLs(portsLookup(ports), hostname, cfg, globalCfg)
				details = strings.Join(subs, "\n")
			}
		}

		rows = append(rows, wsRow{name: name, path: wt.path, branch: wt.branch, orb: orb, status: status, url: url, details: details})
	}
	return rows
}
