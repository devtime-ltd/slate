package scaffold

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/safeio"
)

// DefaultReadOnlyPaths hold what slate generates for the host to trust later:
// the compose files, file-mount payloads, markers and logs under .slate/, and
// the env file compose reads on the host.
var DefaultReadOnlyPaths = []string{".slate/", ".env.container"}

const mountsOverride = "compose.mounts.yaml"

// MountPath is one bind mount over part of the worktree, slash-separated and
// relative to it.
type MountPath struct {
	Path     string
	Dir      bool
	ReadOnly bool
}

const (
	layerDefault = iota
	layerScaffold
	layerProject
)

type mountRule struct {
	readOnly bool
	dir      bool
	layer    int
	replaced bool
}

// ResolveMountPaths combines slate's defaults, the scaffold's paths and the
// project's readonly:/writable: into the mounts to apply. The most specific
// path wins; on the identical path the project beats the scaffold, which
// beats the defaults. A read-only path must be top-level or sit inside
// another read-only path, because a container can swap the directories above
// it for a symlink, which Docker follows on every container start.
func ResolveMountPaths(cfg config.ProjectConfig, s Scaffold) ([]MountPath, []string, error) {
	projectRO, projectRW, disabled, err := cfg.MountPaths()
	if err != nil {
		return nil, nil, err
	}
	if disabled {
		return nil, nil, nil
	}

	rules := map[string]mountRule{}
	add := func(raw string, readOnly bool, layer int, source string) error {
		p, dir, err := normaliseMountPath(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		rule := mountRule{readOnly: readOnly, dir: dir, layer: layer}
		if prev, ok := rules[p]; ok {
			if prev.layer > layer {
				return nil
			}
			if prev.layer == layer && prev.readOnly != readOnly {
				return fmt.Errorf("%s: %q is listed as both read-only and writable", source, raw)
			}
			rule.dir = rule.dir || prev.dir
			rule.replaced = prev.layer < layer && prev.readOnly != readOnly
		}
		rules[p] = rule
		return nil
	}
	sources := []struct {
		paths    []string
		readOnly bool
		layer    int
		source   string
	}{
		{DefaultReadOnlyPaths, true, layerDefault, "slate defaults"},
		{s.ReadOnlyPaths(), true, layerScaffold, "scaffold readonly"},
		{s.WritablePaths(), false, layerScaffold, "scaffold writable"},
		{projectRO, true, layerProject, "slate.yml readonly"},
		{projectRW, false, layerProject, "slate.yml writable"},
	}
	for _, src := range sources {
		for _, raw := range src.paths {
			if err := add(raw, src.readOnly, src.layer, src.source); err != nil {
				return nil, nil, err
			}
		}
	}

	paths := make([]string, 0, len(rules))
	for p := range rules {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		di, dj := strings.Count(paths[i], "/"), strings.Count(paths[j], "/")
		if di != dj {
			return di < dj
		}
		return paths[i] < paths[j]
	})

	var mounts []MountPath
	var warnings []string
	for _, p := range paths {
		rule := rules[p]
		parent, hasParent := nearestRule(rules, p)
		switch {
		case rule.readOnly && !hasParent && strings.Contains(p, "/"),
			rule.readOnly && hasParent && !parent.readOnly:
			return nil, nil, fmt.Errorf("readonly path %q must be a top-level entry or sit inside another read-only path: a container can swap the directories above it", p)
		case rule.readOnly && !hasParent:
			mounts = append(mounts, MountPath{Path: p, Dir: rule.dir, ReadOnly: true})
		case !rule.readOnly && hasParent && parent.readOnly:
			mounts = append(mounts, MountPath{Path: p, Dir: rule.dir})
		case !rule.readOnly && !hasParent && !rule.replaced && rule.layer == layerProject:
			warnings = append(warnings, fmt.Sprintf("writable path %q has no effect: it is not inside a read-only path", p))
		}
	}
	return mounts, warnings, nil
}

func nearestRule(rules map[string]mountRule, p string) (mountRule, bool) {
	for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
		if rule, ok := rules[dir]; ok {
			return rule, true
		}
	}
	return mountRule{}, false
}

func normaliseMountPath(raw string) (string, bool, error) {
	dir := strings.HasSuffix(raw, "/")
	p := path.Clean(strings.TrimSuffix(strings.TrimSpace(raw), "/"))
	if p == "." || p == "" || !filepath.IsLocal(p) {
		return "", false, fmt.Errorf("%q must be a relative path inside the worktree", raw)
	}
	return p, dir, nil
}

type bindMount struct {
	Type     string `yaml:"type"`
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only,omitempty"`
}

type mountsService struct {
	Volumes []bindMount `yaml:"volumes"`
}

// GenerateReadOnlyMounts writes .slate/compose.mounts.yaml, applying mounts
// to every service that binds the worktree (binds maps service to that bind's
// container target). Each path is created when missing and refused when it,
// or a directory above it, is a symlink: Docker would follow it to the host.
func GenerateReadOnlyMounts(workspaceDir string, mounts []MountPath, binds map[string]string) error {
	slateDir := filepath.Join(workspaceDir, ".slate")
	if err := os.MkdirAll(slateDir, 0o755); err != nil {
		return fmt.Errorf("creating .slate dir: %w", err)
	}
	slateFd, err := safeio.OpenDir(slateDir)
	if err != nil {
		return fmt.Errorf("opening .slate: %w", err)
	}
	defer slateFd.Close()

	if err := safeio.RemoveAllAt(slateFd, mountsOverride); err != nil {
		return fmt.Errorf("removing stale %s: %w", mountsOverride, err)
	}
	if len(mounts) == 0 || len(binds) == 0 {
		return nil
	}

	wsFd, err := safeio.OpenDir(workspaceDir)
	if err != nil {
		return fmt.Errorf("opening the worktree: %w", err)
	}
	defer wsFd.Close()
	for _, m := range mounts {
		if err := safeio.EnsurePathAt(wsFd, m.Path, m.Dir); err != nil {
			return fmt.Errorf("cannot mount %s: %w\n\nMake it a regular file or directory inside the worktree, or list it under writable: in slate.yml", m.Path, err)
		}
	}

	services := map[string]mountsService{}
	for service, target := range binds {
		var volumes []bindMount
		for _, m := range mounts {
			volumes = append(volumes, bindMount{
				Type:     "bind",
				Source:   filepath.Join(workspaceDir, filepath.FromSlash(m.Path)),
				Target:   path.Join(target, m.Path),
				ReadOnly: m.ReadOnly,
			})
		}
		services[service] = mountsService{Volumes: volumes}
	}
	data, err := yaml.Marshal(map[string]any{"services": services})
	if err != nil {
		return err
	}
	return safeio.WriteFileAt(slateFd, mountsOverride, data, 0o644)
}

func MountsOverridePath(workspaceDir string) string {
	return filepath.Join(workspaceDir, ".slate", mountsOverride)
}
