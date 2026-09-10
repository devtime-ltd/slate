package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"testing"
)

func TestBuildFrom(t *testing.T) {
	settings := func(revision, at, modified string) []debug.BuildSetting {
		return []debug.BuildSetting{
			{Key: "vcs.revision", Value: revision},
			{Key: "vcs.time", Value: at},
			{Key: "vcs.modified", Value: modified},
		}
	}
	const checkout = "57ce7762d6aa1b2c3d4e5f60718293a4b5c6d7e8"
	cases := []struct {
		name    string
		v, c, d string
		info    *debug.BuildInfo
		want    string
	}{
		{"no build info", "", "", "", nil, "dev (unknown build)"},
		{"buildvcs off", "", "", "", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev (unknown build)"},
		{"untagged checkout", "", "", "",
			&debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20260910114721-57ce7762d6aa+dirty"}, Settings: settings(checkout, "2026-09-10T11:47:21Z", "true")},
			"dev (57ce7762d6aa-dirty 2026-09-10T11:47:21Z)"},
		{"go install @latest", "", "", "",
			&debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20260910114721-57ce7762d6aa"}},
			"dev (57ce7762d6aa 2026-09-10T11:47:21Z)"},
		{"go install @latest after a tag", "", "", "",
			&debug.BuildInfo{Main: debug.Module{Version: "v0.9.1-0.20260910114721-57ce7762d6aa"}},
			"dev (57ce7762d6aa 2026-09-10T11:47:21Z)"},
		{"clean tagged checkout", "", "", "",
			&debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}, Settings: settings(checkout, "2026-09-10T11:47:21Z", "false")},
			"v1.2.3 (57ce7762d6aa 2026-09-10T11:47:21Z)"},
		{"dirty tagged checkout", "", "", "",
			&debug.BuildInfo{Main: debug.Module{Version: "v1.2.3+dirty"}, Settings: settings(checkout, "2026-09-10T11:47:21Z", "true")},
			"v1.2.3 (57ce7762d6aa-dirty 2026-09-10T11:47:21Z)"},
		{"ldflags win over build info", "v9.0.0", "abc1234", "2026-09-10T12:00:00Z",
			&debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}, Settings: settings(checkout, "2026-09-10T11:47:21Z", "true")},
			"v9.0.0 (abc1234-dirty 2026-09-10T12:00:00Z)"},
		{"ldflags without a checkout", "v9.0.0", "", "", &debug.BuildInfo{}, "v9.0.0"},
		{"go install at a tag", "", "", "", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, "v0.1.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildFrom(tc.v, tc.c, tc.d, tc.info).String(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// One installed file is one binary however it is reached (symlink, hard link,
// a directory listed twice, the empty PATH entry meaning the cwd); a second
// real file is what fed the scheduler a stale build.
func TestOtherExecutablesReportsEachOtherFileOnce(t *testing.T) {
	root := t.TempDir()
	dir := func(name string) string {
		d := filepath.Join(root, name)
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		return d
	}
	self := filepath.Join(dir("self"), "slate")
	other := filepath.Join(dir("other"), "slate")
	for _, path := range []string{self, other, filepath.Join(dir("plain"), "slate")} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(root, "plain", "slate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(dir("link"), "slate")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(dir("otherlink"), "slate")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(other, filepath.Join(dir("otherhard"), "slate")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "other"), filepath.Join(root, "otherdir")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(root, "self"))

	dirs := []string{"", ".", filepath.Join(root, "self"), filepath.Join(root, "link"), filepath.Join(root, "other"),
		filepath.Join(root, "otherlink"), filepath.Join(root, "otherhard"), filepath.Join(root, "otherdir"),
		filepath.Join(root, "plain"), filepath.Join(root, "empty"), filepath.Join(root, "other")}
	got := otherExecutables("slate", filepath.Join(root, "link", "slate"), dirs)
	if want := []string{other}; !slices.Equal(got, want) {
		t.Errorf("otherExecutables = %v, want %v", got, want)
	}
}

// The stale binary is named slate, not after the test binary: the scan is
// for what typing slate would run, whatever os.Executable resolved to.
func TestVersionFlagMatchesVersionCommand(t *testing.T) {
	stale := t.TempDir()
	if err := os.WriteFile(filepath.Join(stale, "slate"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stale)

	run := func(args ...string) (string, string) {
		var out, errOut bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&errOut)
		rootCmd.SetArgs(args)
		defer func() {
			rootCmd.SetOut(nil)
			rootCmd.SetErr(nil)
			rootCmd.SetArgs(nil)
			rootCmd.Flags().Set("version", "false")
		}()
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("slate %v: %v", args, err)
		}
		return out.String(), errOut.String()
	}
	cmdOut, cmdErr := run("version")
	flagOut, flagErr := run("--version")
	shortOut, shortErr := run("-v")
	if cmdOut == "" || cmdOut != flagOut || cmdOut != shortOut {
		t.Errorf("version outputs differ: command %q, --version %q, -v %q", cmdOut, flagOut, shortOut)
	}
	if cmdErr == "" || cmdErr != flagErr || cmdErr != shortErr {
		t.Errorf("stale-binary warnings differ: command %q, --version %q, -v %q", cmdErr, flagErr, shortErr)
	}
}
