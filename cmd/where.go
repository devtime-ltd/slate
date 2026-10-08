package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

const defaultDevRoot = "~/Development"

var whereCmd = &cobra.Command{
	Use:   "where [project[@workspace]]",
	Short: "Print a project or workspace directory (pipeable)",
	Long: `Prints the directory of a project, of a workspace inside it, or of the dev
root when given nothing. A project is looked up in the registry first, then
under the dev root as <org>/<repo>, <org>, or a bare <repo> searched across
every org. Failing an exact name, the project, org or workspace whose name
starts with the given one is taken, then the one whose name contains it,
ignoring case: "shop" reaches acme/web-shop. A name that others
only extend past a separator wins over them, so web-shop-iac does not
get in the way; any other tie is listed.

The dev root is ` + defaultDevRoot + ` unless dev_root is set in the global config
or SLATE_DEV_ROOT in the environment.

A process cannot change its parent shell's directory; slate shellenv prints
a function that does, named by --name, with completion.`,
	Args:              cobra.MaximumNArgs(1),
	GroupID:           "tools",
	ValidArgsFunction: completeWhere,
	RunE: func(cmd *cobra.Command, args []string) error {
		if projectOverride != "" || workspaceFlag != "" {
			return fmt.Errorf("where takes project[@workspace] as its argument; --project and --workspace do not apply")
		}
		target := ""
		if len(args) > 0 {
			target = args[0]
		}
		root, err := devRoot()
		if err != nil {
			return err
		}
		registry, err := config.LoadRegistry()
		if err != nil {
			return fmt.Errorf("reading %s: %w", config.RegistryPath(), err)
		}
		dir, err := resolveWhere(target, root, registry)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), dir)
		return nil
	},
}

func devRoot() (string, error) {
	home, _ := os.UserHomeDir()
	if env := os.Getenv("SLATE_DEV_ROOT"); env != "" {
		return devRootFrom(env, "", home), nil
	}
	cfg, err := config.LoadGlobal()
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", filepath.Join(config.GlobalConfigDir(), "config.yml"), err)
	}
	return devRootFrom("", cfg.DevRoot, home), nil
}

func devRootFrom(env, configured, home string) string {
	root := env
	if root == "" {
		root = configured
	}
	if root == "" {
		root = defaultDevRoot
	}
	if root == "~" || strings.HasPrefix(root, "~/") {
		root = filepath.Join(home, root[1:])
	}
	return root
}

func resolveWhere(target, root string, registry map[string]string) (string, error) {
	if target == "" {
		if ok, err := dirExists(root); err != nil {
			return "", err
		} else if !ok {
			return "", missingDevRoot(root)
		}
		return root, nil
	}
	dir, err := resolveProjectExactly(target, root, registry)
	if err == nil {
		return dir, nil
	}
	if !unresolved(err) {
		return "", err
	}
	project, ws, hasAt := splitTarget(target)
	if !hasAt {
		return resolveProjectRoot(target, root, registry)
	}
	if project == "" {
		return "", fmt.Errorf("'%s' needs a project before the @", target)
	}
	projectRoot, err := resolveProjectRoot(project, root, registry)
	if err != nil {
		return "", err
	}
	if ws == "" {
		return projectRoot, nil
	}
	return resolveWorkspaceDir(project, ws, projectRoot)
}

// a whole target is tried as a project before this, so the last @ is the
// separator: workspace names never contain one
func splitTarget(target string) (project, ws string, hasAt bool) {
	i := strings.LastIndex(target, "@")
	if i < 0 {
		return target, "", false
	}
	return target[:i], target[i+1:], true
}

func unresolved(err error) bool {
	var notFound *notFoundError
	var noRoot *missingRootError
	return errors.As(err, &notFound) || errors.As(err, &noRoot)
}

func resolveProjectRoot(project, root string, registry map[string]string) (string, error) {
	dir, err := resolveProjectExactly(project, root, registry)
	if !unresolved(err) {
		return dir, err
	}
	loose, lerr := resolveProjectLoosely(project, root, registry)
	if unresolved(lerr) {
		return "", err
	}
	return loose, lerr
}

func resolveProjectExactly(project, root string, registry map[string]string) (string, error) {
	if path, ok := registry[project]; ok {
		return registeredDir(project, path)
	}
	if ok, err := dirExists(root); err != nil {
		return "", err
	} else if !ok {
		return "", missingDevRoot(root)
	}
	if filepath.IsAbs(project) || !withinRoot(root, project) {
		return "", &notFoundError{name: project, root: root}
	}
	if ok, err := dirExists(filepath.Join(root, project)); err != nil {
		return "", err
	} else if ok {
		return filepath.Join(root, project), nil
	}
	var hits []string
	if !strings.Contains(project, "/") {
		orgs, err := dirNames(root)
		if err != nil {
			return "", err
		}
		for _, org := range orgs {
			ok, err := dirExists(filepath.Join(root, org, project))
			if err != nil {
				return "", err
			}
			if ok {
				hits = append(hits, org+"/"+project)
			}
		}
	}
	switch len(hits) {
	case 0:
		return "", &notFoundError{name: project, root: root}
	case 1:
		return filepath.Join(root, hits[0]), nil
	}
	return "", &ambiguousError{name: project, what: "is in more than one org", hits: hits}
}

func registeredDir(name, path string) (string, error) {
	if ok, err := dirExists(path); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("project '%s' is registered at %s, which does not exist (see %s)", name, path, config.RegistryPath())
	}
	return path, nil
}

func resolveProjectLoosely(project, root string, registry map[string]string) (string, error) {
	if filepath.IsAbs(project) || !withinRoot(root, project) {
		return "", &notFoundError{name: project, root: root}
	}
	pool, token, err := looseCandidates(project, root, registry)
	if err != nil {
		return "", err
	}
	hits := matchLoosely(token, pool)
	switch len(hits) {
	case 0:
		return "", &notFoundError{name: project, root: root}
	case 1:
		if hits[0].registered {
			return registeredDir(hits[0].label, hits[0].path)
		}
		return hits[0].path, nil
	}
	return "", &ambiguousError{name: project, what: "matches more than one project", hits: labels(hits)}
}

func resolveWorkspaceDir(project, ws, projectRoot string) (string, error) {
	wsRoot := workspace.WorkspacesRootIn(projectRoot)
	if workspace.ValidateName(ws) == nil {
		if ok, err := dirExists(filepath.Join(wsRoot, ws)); err != nil {
			return "", err
		} else if ok {
			return filepath.Join(wsRoot, ws), nil
		}
	}
	names, err := dirNames(wsRoot)
	if err != nil {
		return "", err
	}
	var pool []candidate
	for _, name := range names {
		pool = append(pool, candidate{label: name, key: name, path: filepath.Join(wsRoot, name)})
	}
	hits := matchLoosely(ws, pool)
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no workspace '%s' in %s", ws, project)
	case 1:
		return hits[0].path, nil
	}
	return "", &ambiguousError{name: ws, what: "matches more than one workspace in " + project, hits: labels(hits)}
}

type candidate struct {
	label, key, path string
	registered       bool
}

// a registered name stands in for any repo called the same, as for an exact name
func looseCandidates(project, root string, registry map[string]string) ([]candidate, string, error) {
	if org, repo, scoped := strings.Cut(project, "/"); scoped {
		if strings.Contains(repo, "/") {
			return nil, repo, nil
		}
		repos, err := dirNames(filepath.Join(root, org))
		if err != nil {
			return nil, repo, err
		}
		var pool []candidate
		for _, name := range repos {
			pool = append(pool, candidate{label: org + "/" + name, key: name, path: filepath.Join(root, org, name)})
		}
		return pool, repo, nil
	}
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	var pool []candidate
	for _, name := range names {
		pool = append(pool, candidate{label: name, key: name, path: registry[name], registered: true})
	}
	orgs, err := dirNames(root)
	if err != nil {
		return nil, project, err
	}
	for _, org := range orgs {
		if _, taken := registry[org]; !taken {
			pool = append(pool, candidate{label: org, key: org, path: filepath.Join(root, org)})
		}
		repos, err := dirNames(filepath.Join(root, org))
		if err != nil {
			return nil, project, err
		}
		for _, repo := range repos {
			if _, taken := registry[repo]; taken {
				continue
			}
			pool = append(pool, candidate{label: org + "/" + repo, key: repo, path: filepath.Join(root, org, repo)})
		}
	}
	return pool, project, nil
}

func matchLoosely(token string, pool []candidate) []candidate {
	token = strings.ToLower(token)
	if token == "" {
		return nil
	}
	for _, accept := range []func(string, string) bool{strings.HasPrefix, strings.Contains} {
		var hits []candidate
		for _, c := range pool {
			if accept(strings.ToLower(c.key), token) {
				hits = append(hits, c)
			}
		}
		if len(hits) > 0 {
			return withoutExtensions(hits)
		}
	}
	return nil
}

// web-shop-iac extends web-shop: when both match, the base is meant
func withoutExtensions(hits []candidate) []candidate {
	var out []candidate
	for _, hit := range hits {
		extension := false
		for _, base := range hits {
			if extends(hit.key, base.key) {
				extension = true
				break
			}
		}
		if !extension {
			out = append(out, hit)
		}
	}
	return out
}

func extends(name, base string) bool {
	name, base = strings.ToLower(name), strings.ToLower(base)
	return len(name) > len(base) && strings.HasPrefix(name, base) && strings.ContainsRune("-_.", rune(name[len(base)]))
}

func labels(hits []candidate) []string {
	out := make([]string, len(hits))
	for i, hit := range hits {
		out[i] = hit.label
	}
	sort.Strings(out)
	return out
}

type ambiguousError struct {
	name, what string
	hits       []string
}

func (e *ambiguousError) Error() string {
	return fmt.Sprintf("'%s' %s:\n  %s", e.name, e.what, strings.Join(e.hits, "\n  "))
}

func withinRoot(root, name string) bool {
	rel, err := filepath.Rel(root, filepath.Join(root, name))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../")
}

type notFoundError struct {
	name, root string
}

func (e *notFoundError) Error() string {
	return fmt.Sprintf("nothing called '%s' under %s", e.name, e.root)
}

type missingRootError struct {
	root string
}

func (e *missingRootError) Error() string {
	return fmt.Sprintf("dev root %s does not exist; set dev_root in %s or SLATE_DEV_ROOT", e.root, filepath.Join(config.GlobalConfigDir(), "config.yml"))
}

func missingDevRoot(root string) error {
	return &missingRootError{root: root}
}

func completeWhere(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	root, err := devRoot()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	registry, err := config.LoadRegistry()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return whereCandidates(toComplete, root, registry), cobra.ShellCompDirectiveNoFileComp
}

func whereCandidates(token, root string, registry map[string]string) []string {
	seen := map[string]bool{}
	for name := range registry {
		seen[name] = true
	}
	for _, org := range subdirs(root) {
		seen[org] = true
		for _, repo := range subdirs(filepath.Join(root, org)) {
			seen[repo] = true
			seen[org+"/"+repo] = true
		}
	}
	if project, _, hasAt := splitTarget(token); hasAt {
		projectRoot, err := resolveProjectExactly(project, root, registry)
		// foo@ is foo@bar half-typed, not project foo with no workspace yet
		if err != nil && !startsAny(token, seen) {
			projectRoot, err = resolveProjectRoot(project, root, registry)
		}
		if err == nil {
			for _, ws := range subdirs(workspace.WorkspacesRootIn(projectRoot)) {
				seen[project+"@"+ws] = true
			}
		}
	}
	var out []string
	for c := range seen {
		if strings.HasPrefix(c, token) {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

func startsAny(token string, names map[string]bool) bool {
	for name := range names {
		if strings.HasPrefix(name, token) {
			return true
		}
	}
	return false
}

func subdirs(dir string) []string {
	names, _ := dirNames(dir)
	return names
}

// dirNames lists the directories in dir, skipping hidden ones; a missing dir
// has none, and any other failure, such as a permission error, is returned
func dirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if ok, err := dirExists(filepath.Join(dir, name)); err != nil {
			return nil, err
		} else if ok {
			out = append(out, name)
		}
	}
	return out, nil
}

// dirExists reports a missing path or a non-directory as false and any other
// failure, such as a permission error, as an error rather than as absence
func dirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.IsDir(), nil
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	return false, err
}

func init() {
	rootCmd.AddCommand(whereCmd)
}
