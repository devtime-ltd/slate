package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func devRootFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{
		"centerframe/sparta/.slate/workspaces/redis-cache",
		"centerframe/sparta/.slate/workspaces/bulk-invite",
		"devtime-ltd/slate",
		"devtime-ltd/hydra",
		"4lun/hydra",
		"4lun/at@sign/.slate/workspaces/ws1",
		"4lun/dup@x",
		"devtime-ltd/dup@x",
		"acme/web-shop/.slate/workspaces/feature-x",
		"acme/web-shop-iac",
		"4lun/flint-nixos",
		"4lun/prism-nixos",
		"4lun/nixos-machines",
		".hidden/secret",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "centerframe", "notes.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolveWhere(t *testing.T) {
	root := devRootFixture(t)
	registered := t.TempDir()
	atSign := t.TempDir()
	if err := os.MkdirAll(filepath.Join(atSign, ".slate", "workspaces", "redis-cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := map[string]string{"vesper": registered, "gone": filepath.Join(root, "nowhere"), "foo@bar": atSign}
	sparta := filepath.Join(root, "centerframe", "sparta")
	shop := filepath.Join(root, "acme", "web-shop")

	cases := []struct {
		target  string
		want    string
		wantErr string
	}{
		{"", root, ""},
		{"vesper", registered, ""},
		{"foo@bar", atSign, ""},
		{"foo@bar@redis-cache", filepath.Join(atSign, ".slate", "workspaces", "redis-cache"), ""},
		{"foo@bar@nope", "", "no workspace 'nope' in foo@bar"},
		{"at@sign", filepath.Join(root, "4lun", "at@sign"), ""},
		{"4lun/at@sign", filepath.Join(root, "4lun", "at@sign"), ""},
		{"at@sign@ws1", filepath.Join(root, "4lun", "at@sign", ".slate", "workspaces", "ws1"), ""},
		{"at@sign@nope", "", "no workspace 'nope' in at@sign"},
		{"at@si", "", "no workspace 'si' in at"},
		{"foo@ba", "", "no workspace 'ba' in foo"},
		{"dup@x", "", "'dup@x' is in more than one org"},
		{"sparta", sparta, ""},
		{"centerframe/sparta", sparta, ""},
		{"centerframe", filepath.Join(root, "centerframe"), ""},
		{"sparta@redis-cache", filepath.Join(sparta, ".slate", "workspaces", "redis-cache"), ""},
		{"sparta@", sparta, ""},
		{"centerframe/sparta@bulk-invite", filepath.Join(sparta, ".slate", "workspaces", "bulk-invite"), ""},
		{"hydra", "", "'hydra' is in more than one org"},
		{"shop", shop, ""},
		{"SHOP", shop, ""},
		{"web", shop, ""},
		{"iac", filepath.Join(root, "acme", "web-shop-iac"), ""},
		{"acme/shop", shop, ""},
		{"acme/WEB", shop, ""},
		{"nixos", filepath.Join(root, "4lun", "nixos-machines"), ""},
		{"acm", filepath.Join(root, "acme"), ""},
		{"spar", sparta, ""},
		{"vesp", registered, ""},
		{"gon", "", "does not exist"},
		{"shop@feat", filepath.Join(shop, ".slate", "workspaces", "feature-x"), ""},
		{"shop@nope", "", "no workspace 'nope' in shop"},
		{"sparta@red", filepath.Join(sparta, ".slate", "workspaces", "redis-cache"), ""},
		{"sparta@B", filepath.Join(sparta, ".slate", "workspaces", "bulk-invite"), ""},
		{"spar@red", filepath.Join(sparta, ".slate", "workspaces", "redis-cache"), ""},
		{"sparta@e", "", "'e' matches more than one workspace in sparta"},
		{"-nixos", "", "'-nixos' matches more than one project"},
		{"hyd", "", "'hyd' matches more than one project"},
		{"s", "", "'s' matches more than one project"},
		{"notes", "", "nothing called 'notes'"},
		{"acme/nope", "", "nothing called 'acme/nope'"},
		{"nope/shop", "", "nothing called 'nope/shop'"},
		{"a/b/c", "", "nothing called 'a/b/c'"},
		{"nope", "", "nothing called 'nope'"},
		{"centerframe/nope", "", "nothing called 'centerframe/nope'"},
		{"notes.md", "", "nothing called 'notes.md'"},
		{"secret", "", "nothing called 'secret'"},
		{"sparta@nope", "", "no workspace 'nope' in sparta"},
		{"sparta@../..", "", "no workspace '../..' in sparta"},
		{"sparta@..", "", "no workspace '..' in sparta"},
		{"..", "", "nothing called '..'"},
		{".", "", "nothing called '.'"},
		{"../" + filepath.Base(root), "", "nothing called"},
		{"centerframe/..", "", "nothing called 'centerframe/..'"},
		{"/centerframe/sparta", "", "nothing called '/centerframe/sparta'"},
		{root + "/centerframe/sparta", "", "nothing called"},
		{"@redis-cache", "", "needs a project before the @"},
		{"gone", "", "does not exist"},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			got, err := resolveWhere(tc.target, root, registry)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveWhereListsEveryOrgOfAnAmbiguousName(t *testing.T) {
	root := devRootFixture(t)
	for name, orgs := range map[string][]string{
		"hydra":  {"4lun/hydra", "devtime-ltd/hydra"},
		"dup@x":  {"4lun/dup@x", "devtime-ltd/dup@x"},
		"-nixos": {"4lun/flint-nixos", "4lun/prism-nixos"},
		"hyd":    {"4lun/hydra", "devtime-ltd/hydra"},
		"s":      {"centerframe/sparta", "devtime-ltd/slate"},
	} {
		_, err := resolveWhere(name, root, nil)
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		for _, org := range orgs {
			if !strings.Contains(err.Error(), org) {
				t.Errorf("%s: error %q does not name %s", name, err, org)
			}
		}
	}
}

func TestResolveWhereLetsARegisteredNameStandInForItsRepo(t *testing.T) {
	root := devRootFixture(t)
	elsewhere := t.TempDir()
	registry := map[string]string{"web-shop": elsewhere}
	for _, target := range []string{"shop", "web"} {
		if got, err := resolveWhere(target, root, registry); err != nil || got != elsewhere {
			t.Errorf("%s: got %q, %v, want the registered path with no tie against acme/web-shop", target, got, err)
		}
	}
}

func TestResolveWhereTakesAnExactWorkspaceOverAPartialProject(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"o/foo/.slate/workspaces/bar", "o/foo@bar-tools"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tools := filepath.Join(root, "o", "foo@bar-tools")
	for target, want := range map[string]string{
		"foo@bar":       filepath.Join(root, "o", "foo", ".slate", "workspaces", "bar"),
		"foo@bar-tools": tools,
		"bar-t":         tools,
	} {
		if got, err := resolveWhere(target, root, nil); err != nil || got != want {
			t.Errorf("%s: got %q, %v, want %q", target, got, err, want)
		}
	}
	if got, want := whereCandidates("foo@", root, nil), []string{"foo@bar", "foo@bar-tools"}; !reflect.DeepEqual(got, want) {
		t.Errorf("completion: got %q, want %q", got, want)
	}
}

func TestExtends(t *testing.T) {
	cases := []struct {
		name, base string
		want       bool
	}{
		{"web-shop-iac", "web-shop", true},
		{"Web-Shop-IAC", "web-shop", true},
		{"static.0c03.com", "static", true},
		{"a_b", "a", true},
		{"shops", "web", false},
		{"web", "web", false},
		{"web", "web-shop", false},
		{"shop-web", "web", false},
	}
	for _, tc := range cases {
		if got := extends(tc.name, tc.base); got != tc.want {
			t.Errorf("extends(%q, %q) = %v, want %v", tc.name, tc.base, got, tc.want)
		}
	}
}

func TestResolveWhereTreatsAStaleExactRegistryNameAsAuthoritative(t *testing.T) {
	foo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(foo, ".slate", "workspaces", "bar"), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := map[string]string{"foo": foo, "foo@bar": filepath.Join(t.TempDir(), "gone")}
	_, err := resolveWhere("foo@bar", devRootFixture(t), registry)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want the stale registry entry reported", err)
	}
}

func TestResolveWhereReportsAnUnreadableOrg(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	for _, dir := range []string{"open/thing", "locked/thing"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	_, err := resolveWhere("thing", root, nil)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want the permission failure surfaced, not a guess", err)
	}

	registered := t.TempDir()
	if err := os.MkdirAll(filepath.Join(registered, ".slate", "workspaces", "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = resolveWhere("thing@ws", root, map[string]string{"thing": registered})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("thing@ws: err = %v, want the whole-target probe's failure surfaced before splitting", err)
	}
}

func TestResolveWhereWithoutADevRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	registered := t.TempDir()
	registry := map[string]string{"vesper": registered}

	for _, target := range []string{"vesper", "vesp", "ESPE"} {
		if got, err := resolveWhere(target, root, registry); err != nil || got != registered {
			t.Errorf("%q: got %q, %v, want the registered project without a dev root", target, got, err)
		}
	}
	for _, target := range []string{"", "sparta", "org/vesp"} {
		if _, err := resolveWhere(target, root, registry); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("%q: err = %v, want the missing root named", target, err)
		}
	}
}

func TestWhereCandidates(t *testing.T) {
	root := devRootFixture(t)
	atSign := t.TempDir()
	if err := os.MkdirAll(filepath.Join(atSign, ".slate", "workspaces", "redis-cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := map[string]string{"vesper": t.TempDir(), "foo@bar": atSign}

	cases := []struct {
		token string
		want  []string
	}{
		{"", []string{"4lun", "4lun/at@sign", "4lun/dup@x", "4lun/flint-nixos", "4lun/hydra", "4lun/nixos-machines", "4lun/prism-nixos", "acme", "acme/web-shop", "acme/web-shop-iac", "at@sign", "centerframe", "centerframe/sparta", "devtime-ltd", "devtime-ltd/dup@x", "devtime-ltd/hydra", "devtime-ltd/slate", "dup@x", "flint-nixos", "foo@bar", "hydra", "nixos-machines", "prism-nixos", "slate", "sparta", "vesper", "web-shop", "web-shop-iac"}},
		{"foo@", []string{"foo@bar"}},
		{"foo@bar@", []string{"foo@bar@redis-cache"}},
		{"at@", []string{"at@sign"}},
		{"at@sign@", []string{"at@sign@ws1"}},
		{"spa", []string{"sparta"}},
		{"devtime-ltd/", []string{"devtime-ltd/dup@x", "devtime-ltd/hydra", "devtime-ltd/slate"}},
		{"sparta@", []string{"sparta@bulk-invite", "sparta@redis-cache"}},
		{"sparta@red", []string{"sparta@redis-cache"}},
		{"centerframe/sparta@b", []string{"centerframe/sparta@bulk-invite"}},
		{"shop@", []string{"shop@feature-x"}},
		{"spar@red", []string{"spar@redis-cache"}},
		{"dup@", []string{"dup@x"}},
		{"hydra@", nil},
		{"nope@", nil},
		{"zzz", nil},
	}
	for _, tc := range cases {
		t.Run(tc.token, func(t *testing.T) {
			if got := whereCandidates(tc.token, root, registry); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDevRootReportsAMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SLATE_CONFIG_DIR", dir)
	t.Setenv("SLATE_DEV_ROOT", "")
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("dev_root: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := devRoot(); err == nil || !strings.Contains(err.Error(), "config.yml") {
		t.Fatalf("err = %v, want the config file named", err)
	}
	t.Setenv("SLATE_DEV_ROOT", "/srv/override")
	if root, err := devRoot(); err != nil || root != "/srv/override" {
		t.Fatalf("with the override set: root = %q, %v, want the override to bypass the broken config", root, err)
	}
	t.Setenv("SLATE_DEV_ROOT", "")
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("dev_root: /srv/code\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if root, err := devRoot(); err != nil || root != "/srv/code" {
		t.Fatalf("root = %q, %v", root, err)
	}
}

func TestWhereRejectsTheGlobalSelectors(t *testing.T) {
	for name, set := range map[string]func(){
		"project":   func() { projectOverride = "sparta" },
		"workspace": func() { workspaceFlag = "api" },
	} {
		t.Run(name, func(t *testing.T) {
			set()
			defer func() { projectOverride = ""; workspaceFlag = "" }()
			err := whereCmd.RunE(whereCmd, nil)
			if err == nil || !strings.Contains(err.Error(), "do not apply") {
				t.Fatalf("err = %v, want the selectors refused", err)
			}
		})
	}
}

func TestDevRootFrom(t *testing.T) {
	cases := []struct {
		name, env, configured, want string
	}{
		{"default", "", "", "/home/u/Development"},
		{"config", "", "~/Code", "/home/u/Code"},
		{"config absolute", "", "/srv/code", "/srv/code"},
		{"env wins", "/tmp/dev", "~/Code", "/tmp/dev"},
		{"bare tilde", "~", "", "/home/u"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := devRootFrom(tc.env, tc.configured, "/home/u"); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
