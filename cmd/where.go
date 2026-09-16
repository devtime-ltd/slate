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
every org.

The dev root is ` + defaultDevRoot + ` unless dev_root is set in the global config
or SLATE_DEV_ROOT in the environment.

A process cannot change its parent shell's directory; slate shellenv prints
a dev function that does, with completion.`,
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
	if _, registered := registry[target]; registered {
		return resolveProjectRoot(target, root, registry)
	}
	dir, err := resolveProjectRoot(target, root, registry)
	if err == nil {
		return dir, nil
	}
	var notFound *notFoundError
	var noRoot *missingRootError
	if !errors.As(err, &notFound) && !errors.As(err, &noRoot) {
		return "", err
	}
	project, ws, _ := splitTarget(target)
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
	wsDir := filepath.Join(workspace.WorkspacesRootIn(projectRoot), ws)
	if workspace.ValidateName(ws) != nil {
		return "", fmt.Errorf("no workspace '%s' in %s", ws, project)
	}
	if ok, err := dirExists(wsDir); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("no workspace '%s' in %s", ws, project)
	}
	return wsDir, nil
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

func resolveProjectRoot(project, root string, registry map[string]string) (string, error) {
	if path, ok := registry[project]; ok {
		if ok, err := dirExists(path); err != nil {
			return "", err
		} else if !ok {
			return "", fmt.Errorf("project '%s' is registered at %s, which does not exist (see %s)", project, path, config.RegistryPath())
		}
		return path, nil
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
		orgs, err := os.ReadDir(root)
		if err != nil {
			return "", err
		}
		for _, org := range orgs {
			if strings.HasPrefix(org.Name(), ".") {
				continue
			}
			ok, err := dirExists(filepath.Join(root, org.Name(), project))
			if err != nil {
				return "", err
			}
			if ok {
				hits = append(hits, org.Name()+"/"+project)
			}
		}
	}
	switch len(hits) {
	case 0:
		return "", &notFoundError{name: project, root: root}
	case 1:
		return filepath.Join(root, hits[0]), nil
	}
	return "", &ambiguousError{name: project, hits: hits}
}

type ambiguousError struct {
	name string
	hits []string
}

func (e *ambiguousError) Error() string {
	return fmt.Sprintf("'%s' is in more than one org:\n  %s", e.name, strings.Join(e.hits, "\n  "))
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
		if projectRoot, err := resolveProjectRoot(project, root, registry); err == nil {
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

func subdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !isDir(filepath.Join(dir, name)) {
			continue
		}
		out = append(out, name)
	}
	return out
}

func isDir(path string) bool {
	ok, _ := dirExists(path)
	return ok
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
