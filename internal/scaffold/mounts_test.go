package scaffold

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/devtime-ltd/slate/internal/config"
)

type pathsScaffold struct {
	nullScaffold
	ro, rw []string
}

func (s pathsScaffold) ReadOnlyPaths() []string { return s.ro }
func (s pathsScaffold) WritablePaths() []string { return s.rw }

func projectCfg(t *testing.T, src string) config.ProjectConfig {
	t.Helper()
	var cfg config.ProjectConfig
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func mountSummary(mounts []MountPath) []string {
	var out []string
	for _, m := range mounts {
		mode := "rw"
		if m.ReadOnly {
			mode = "ro"
		}
		out = append(out, m.Path+":"+mode)
	}
	return out
}

func TestResolveMountPaths(t *testing.T) {
	laravel, err := Get("laravel")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		project  string
		scaffold Scaffold
		want     []string
	}{
		{
			name:     "defaults with the scaffold's caches reopened",
			scaffold: laravel,
			want:     []string{".env.container:ro", ".slate:ro", ".slate/composer:rw", ".slate/npm-cache:rw"},
		},
		{
			name:     "project additions",
			project:  "readonly: [.idea/]\nwritable: [.slate/tmp/]\n",
			scaffold: laravel,
			want:     []string{".env.container:ro", ".idea:ro", ".slate:ro", ".slate/composer:rw", ".slate/npm-cache:rw", ".slate/tmp:rw"},
		},
		{
			name:     "the project wins on the identical path",
			project:  "readonly: [.slate/composer/]\n",
			scaffold: laravel,
			want:     []string{".env.container:ro", ".slate:ro", ".slate/npm-cache:rw"},
		},
		{
			name:     "the most specific path wins over the layer",
			project:  "readonly: [.slate/]\n",
			scaffold: laravel,
			want:     []string{".env.container:ro", ".slate:ro", ".slate/composer:rw", ".slate/npm-cache:rw"},
		},
		{
			name:     "writable removes a default and what sat inside it",
			project:  "writable: [.slate/]\n",
			scaffold: laravel,
			want:     []string{".env.container:ro"},
		},
		{
			name:     "readonly: false switches everything off",
			project:  "readonly: false\n",
			scaffold: laravel,
			want:     nil,
		},
		{
			name:     "a read-only path inside another adds no mount",
			project:  "readonly: [.slate/files/]\n",
			scaffold: &nullScaffold{},
			want:     []string{".env.container:ro", ".slate:ro"},
		},
		{
			name:     "scaffold read-only paths join the defaults",
			scaffold: pathsScaffold{ro: []string{".claude/"}},
			want:     []string{".claude:ro", ".env.container:ro", ".slate:ro"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mounts, warnings, err := ResolveMountPaths(projectCfg(t, tc.project), tc.scaffold)
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 0 {
				t.Errorf("unexpected warnings: %v", warnings)
			}
			got := mountSummary(mounts)
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestResolveMountPathsRefusesSwappableReadOnlyPaths(t *testing.T) {
	for _, project := range []string{
		"readonly: [docs/CLAUDE.md]\n",
		"writable: [.slate/cache/]\nreadonly: [.slate/cache/inner]\n",
	} {
		if _, _, err := ResolveMountPaths(projectCfg(t, project), &nullScaffold{}); err == nil || !strings.Contains(err.Error(), "top-level") {
			t.Errorf("%q: want a top-level error, got %v", project, err)
		}
	}
}

func TestResolveMountPathsRejectsBadPaths(t *testing.T) {
	for _, project := range []string{
		"readonly: [../outside]\n",
		"readonly: [/etc]\n",
		"writable: [.]\n",
		"readonly: [.idea/]\nwritable: [.idea/]\n",
	} {
		if _, _, err := ResolveMountPaths(projectCfg(t, project), &nullScaffold{}); err == nil {
			t.Errorf("%q should be rejected", project)
		}
	}
}

func TestResolveMountPathsWarnsOnWritableWithNoEffect(t *testing.T) {
	_, warnings, err := ResolveMountPaths(projectCfg(t, "writable: [storage/]\n"), &nullScaffold{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "storage") {
		t.Errorf("want one warning naming storage, got %v", warnings)
	}

	_, warnings, _ = ResolveMountPaths(projectCfg(t, "writable: [.env.container]\n"), &nullScaffold{})
	if len(warnings) != 0 {
		t.Errorf("removing a default is an effect, got warnings %v", warnings)
	}
}

func TestGenerateReadOnlyMountsCoversEveryWorktreeService(t *testing.T) {
	ws := t.TempDir()
	mounts := []MountPath{
		{Path: ".slate", Dir: true, ReadOnly: true},
		{Path: ".env.container", ReadOnly: true},
		{Path: ".slate/npm-cache", Dir: true},
	}
	binds := map[string]string{"app": "/app", "queue": "/app", "vite": "/srv"}
	if err := GenerateReadOnlyMounts(ws, mounts, binds); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(MountsOverridePath(ws))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]struct {
			Volumes []bindMount `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Services) != 3 {
		t.Fatalf("want app, queue and vite, got %v", doc.Services)
	}
	vite := doc.Services["vite"].Volumes
	want := []bindMount{
		{Type: "bind", Source: filepath.Join(ws, ".slate"), Target: "/srv/.slate", ReadOnly: true},
		{Type: "bind", Source: filepath.Join(ws, ".env.container"), Target: "/srv/.env.container", ReadOnly: true},
		{Type: "bind", Source: filepath.Join(ws, ".slate/npm-cache"), Target: "/srv/.slate/npm-cache"},
	}
	if !slices.Equal(vite, want) {
		t.Errorf("vite volumes = %+v, want %+v", vite, want)
	}

	if info, err := os.Stat(filepath.Join(ws, ".slate/npm-cache")); err != nil || !info.IsDir() {
		t.Errorf("missing writable dir should be created (err=%v)", err)
	}
	if info, err := os.Stat(filepath.Join(ws, ".env.container")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("missing read-only file should be created (err=%v)", err)
	}
}

func TestGenerateReadOnlyMountsRefusesSymlinks(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, ".claude")); err != nil {
		t.Fatal(err)
	}
	mounts := []MountPath{{Path: ".claude", Dir: true, ReadOnly: true}}
	err := GenerateReadOnlyMounts(ws, mounts, map[string]string{"app": "/app"})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a symlink refusal, got %v", err)
	}
	if _, err := os.Stat(MountsOverridePath(ws)); !os.IsNotExist(err) {
		t.Errorf("no override may be written after a refusal (err=%v)", err)
	}
}

func TestGenerateReadOnlyMountsClearsStaleOverride(t *testing.T) {
	ws := t.TempDir()
	mounts := []MountPath{{Path: ".slate", Dir: true, ReadOnly: true}}
	if err := GenerateReadOnlyMounts(ws, mounts, map[string]string{"app": "/app"}); err != nil {
		t.Fatal(err)
	}
	if err := GenerateReadOnlyMounts(ws, nil, map[string]string{"app": "/app"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(MountsOverridePath(ws)); !os.IsNotExist(err) {
		t.Errorf("stale override should be removed (err=%v)", err)
	}
}
