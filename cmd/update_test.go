package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

type releaseServer struct {
	*httptest.Server
	hits atomic.Int32
}

func newReleaseServer(t *testing.T, handler http.HandlerFunc) *releaseServer {
	t.Helper()
	s := &releaseServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func releaseJSON(tag string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://example.test"}`))
	}
}

// clock is a settable now for the cooldown tests.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestUpdate(t *testing.T, running, url string, c *clock) *updateCheck {
	t.Helper()
	return &updateCheck{running: running, stateDir: t.TempDir(), url: url, now: c.now}
}

func runUpdate(u *updateCheck) string {
	var out bytes.Buffer
	u.start()
	u.finish(&out)
	return out.String()
}

func TestUpdateNoticeOnceADay(t *testing.T) {
	server := newReleaseServer(t, releaseJSON("v0.2.0"))
	c := &clock{time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
	u := newTestUpdate(t, "v0.1.0", server.URL, c)

	want := "\nslate v0.2.0 is available (you have v0.1.0): go install github.com/devtime-ltd/slate@latest\n"
	if got := runUpdate(u); got != want {
		t.Fatalf("first run: got %q, want %q", got, want)
	}
	if state := u.load(); state.Latest != "v0.2.0" || !state.CheckedAt.Equal(c.t) || !state.NotifiedAt.Equal(c.t) {
		t.Errorf("state after the first run: %+v", state)
	}

	c.t = c.t.Add(time.Hour)
	if got := runUpdate(u); got != "" {
		t.Errorf("an hour later: got %q, want nothing", got)
	}
	if n := server.hits.Load(); n != 1 {
		t.Errorf("the cached answer should have been used, server hit %d times", n)
	}

	c.t = c.t.Add(24 * time.Hour)
	if got := runUpdate(u); got != want {
		t.Errorf("a day later: got %q, want %q", got, want)
	}
	if n := server.hits.Load(); n != 2 {
		t.Errorf("a day later the check should have run again, server hit %d times", n)
	}
}

func TestUpdateNoticeQuietWhenCurrent(t *testing.T) {
	for _, latest := range []string{"v0.1.0", "v0.0.9", "v0.1.0-rc1"} {
		t.Run(latest, func(t *testing.T) {
			server := newReleaseServer(t, releaseJSON(latest))
			u := newTestUpdate(t, "v0.1.0", server.URL, &clock{time.Now()})
			if got := runUpdate(u); got != "" {
				t.Errorf("got %q, want nothing", got)
			}
			if state := u.load(); state.Latest != latest {
				t.Errorf("the answer should still be cached, state %+v", state)
			}
		})
	}
}

// The command must not wait on GitHub: a fetch that has not answered within
// the budget is abandoned, and the attempt still counts so the next command
// does not pay again.
func TestUpdateCheckNeverBlocksBeyondBudget(t *testing.T) {
	release := make(chan struct{})
	server := newReleaseServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	// registered after the server so it runs first: Close waits for handlers
	t.Cleanup(func() { close(release) })
	c := &clock{time.Now()}
	u := newTestUpdate(t, "v0.1.0", server.URL, c)

	started := time.Now()
	got := runUpdate(u)
	if took := time.Since(started); took > updateWaitBudget+500*time.Millisecond {
		t.Errorf("finish waited %s, budget is %s", took, updateWaitBudget)
	}
	if got != "" {
		t.Errorf("got %q, want nothing", got)
	}
	state := u.load()
	if state.AttemptedAt.IsZero() || state.Latest != "" || !state.CheckedAt.IsZero() {
		t.Errorf("an abandoned fetch should record only the attempt, state %+v", state)
	}

	c.t = c.t.Add(30 * time.Minute)
	u2 := &updateCheck{running: "v0.1.0", stateDir: u.stateDir, url: server.URL, now: c.now}
	runUpdate(u2)
	if n := server.hits.Load(); n != 1 {
		t.Errorf("no retry within the hour, server hit %d times", n)
	}

	c.t = c.t.Add(time.Hour)
	runUpdate(u2)
	if n := server.hits.Load(); n != 2 {
		t.Errorf("a retry after the hour, server hit %d times", n)
	}
}

func TestUpdateCheckIgnoresBadAnswers(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"server error": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"no release":   func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) },
		"captive portal": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("<html>sign in to the wifi</html>"))
		},
		"tag is not a version": releaseJSON("nightly"),
		"tag without v":        releaseJSON("0.2.0"),
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			server := newReleaseServer(t, handler)
			c := &clock{time.Now()}
			u := newTestUpdate(t, "v0.1.0", server.URL, c)
			if got := runUpdate(u); got != "" {
				t.Errorf("got %q, want nothing", got)
			}
			if state := u.load(); state.Latest != "" || !state.CheckedAt.IsZero() || state.AttemptedAt.IsZero() {
				t.Errorf("a bad answer should record only the attempt, state %+v", state)
			}
			c.t = c.t.Add(30 * time.Minute)
			runUpdate(u)
			if n := server.hits.Load(); n != 1 {
				t.Errorf("no retry within the hour, server hit %d times", n)
			}
		})
	}
}

func TestUpdateCheckUnreachable(t *testing.T) {
	server := newReleaseServer(t, releaseJSON("v0.2.0"))
	server.Close()
	u := newTestUpdate(t, "v0.1.0", server.URL, &clock{time.Now()})
	started := time.Now()
	if got := runUpdate(u); got != "" {
		t.Errorf("got %q, want nothing", got)
	}
	if took := time.Since(started); took > updateWaitBudget {
		t.Errorf("a refused connection took %s", took)
	}
}

func TestUpdateCheckSurvivesACorruptStateFile(t *testing.T) {
	server := newReleaseServer(t, releaseJSON("v0.2.0"))
	u := newTestUpdate(t, "v0.1.0", server.URL, &clock{time.Now()})
	if err := os.WriteFile(u.statePath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := runUpdate(u); !strings.Contains(got, "v0.2.0 is available") {
		t.Errorf("got %q", got)
	}
	if state := u.load(); state.Latest != "v0.2.0" {
		t.Errorf("the corrupt file should have been replaced, state %+v", state)
	}
}

func TestUpdateCheckNeedsAWritableStateDir(t *testing.T) {
	server := newReleaseServer(t, releaseJSON("v0.2.0"))
	u := newTestUpdate(t, "v0.1.0", server.URL, &clock{time.Now()})
	// a directory under a regular file cannot be made, whoever runs the tests
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	u.stateDir = filepath.Join(file, "slate")
	if got := runUpdate(u); got != "" {
		t.Errorf("got %q, want nothing", got)
	}
	if n := server.hits.Load(); n != 0 {
		t.Errorf("nowhere to record the attempt, yet the server was hit %d times", n)
	}
}

func TestSaveLeavesNoTempFileBehind(t *testing.T) {
	u := newTestUpdate(t, "v0.1.0", "", &clock{time.Now()})
	// a non-empty directory at the state path makes the rename fail
	if err := os.MkdirAll(filepath.Join(u.statePath(), "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := u.save(updateState{Latest: "v0.2.0"}); err == nil {
		t.Fatal("save succeeded onto a directory")
	}
	entries, err := os.ReadDir(u.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "update-check.json" {
			t.Errorf("left %s behind", e.Name())
		}
	}
}

// A cached answer with nowhere to record the notice would repeat it on
// every command.
func TestUpdateNoticeNeedsARecordedNotice(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a 0555 directory")
	}
	server := newReleaseServer(t, releaseJSON("v0.2.0"))
	c := &clock{time.Now()}
	u := newTestUpdate(t, "v0.1.0", server.URL, c)
	if err := u.save(updateState{CheckedAt: c.t, Latest: "v0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(u.stateDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(u.stateDir, 0o755) })
	for i := range 3 {
		if got := runUpdate(u); got != "" {
			t.Errorf("run %d: got %q, want nothing", i, got)
		}
	}
}

func TestUpdateCheckTreatsAFutureStampAsStale(t *testing.T) {
	server := newReleaseServer(t, releaseJSON("v0.1.0"))
	c := &clock{time.Now()}
	u := newTestUpdate(t, "v0.1.0", server.URL, c)
	ahead := c.t.Add(365 * 24 * time.Hour)
	if err := u.save(updateState{AttemptedAt: ahead, CheckedAt: ahead, Latest: "v0.1.0"}); err != nil {
		t.Fatal(err)
	}
	runUpdate(u)
	if n := server.hits.Load(); n != 1 {
		t.Errorf("a stamp from the future should not count as recent, server hit %d times", n)
	}
	if state := u.load(); !state.CheckedAt.Equal(c.t) {
		t.Errorf("the stamp should have been replaced, state %+v", state)
	}
}

// Most slate commands outlive the budget. The answer has long since arrived
// by then and must be taken, not raced against an already expired timer.
func TestUpdateNoticeSurvivesALongCommand(t *testing.T) {
	server := newReleaseServer(t, releaseJSON("v0.2.0"))
	for i := range 20 {
		c := &clock{time.Now()}
		u := newTestUpdate(t, "v0.1.0", server.URL, c)
		u.start()
		deadline := time.Now().Add(5 * time.Second)
		for len(u.result) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		started := c.t
		c.t = c.t.Add(45 * time.Second)
		var out bytes.Buffer
		u.finish(&out)
		if !strings.Contains(out.String(), "v0.2.0 is available") {
			t.Fatalf("run %d: got %q", i, out.String())
		}
		if state := u.load(); state.Latest != "v0.2.0" || !state.CheckedAt.Equal(started) {
			t.Fatalf("run %d: the answer should be cached as of the fetch, state %+v", i, state)
		}
	}
}

func TestUpdateCheckKeepsAFresherAnswer(t *testing.T) {
	server := newReleaseServer(t, releaseJSON("v0.2.0"))
	c := &clock{time.Now()}
	u := newTestUpdate(t, "v0.1.0", server.URL, c)
	u.start()
	deadline := time.Now().Add(5 * time.Second)
	for len(u.result) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// another command checked an hour into this one and found a newer release
	c.t = c.t.Add(time.Hour)
	checked := c.t
	if err := u.save(updateState{CheckedAt: checked, Latest: "v0.3.0"}); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(time.Hour)
	var out bytes.Buffer
	u.finish(&out)
	if !strings.Contains(out.String(), "v0.3.0 is available") {
		t.Errorf("got %q, want the fresher release", out.String())
	}
	if state := u.load(); state.Latest != "v0.3.0" || !state.CheckedAt.Equal(checked) {
		t.Errorf("the fresher answer should stand, state %+v", state)
	}
}

func TestWantsUpdateCheck(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		terminal bool
		want     bool
	}{
		{"terminal", nil, true, true},
		{"stderr is a pipe", nil, false, false},
		{"opted out", map[string]string{"SLATE_NO_UPDATE_CHECK": "1"}, true, false},
		{"agent session", map[string]string{"SLATE_AGENT": "1"}, true, false},
		{"other SLATE_AGENT value", map[string]string{"SLATE_AGENT": "0"}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(key string) string { return tc.env[key] }
			if got := wantsUpdateCheck(getenv, tc.terminal); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUpdateCheckForADevBuild(t *testing.T) {
	if updateCheckFor("") != nil {
		t.Error("a build without a tag should get no check")
	}
	if u := updateCheckFor("v0.1.0"); u == nil || u.running != "v0.1.0" {
		t.Errorf("got %+v", u)
	}
}

func TestCachedNotice(t *testing.T) {
	if got := (*updateCheck)(nil).cachedNotice(); got != "" {
		t.Errorf("nil check: got %q", got)
	}
	u := newTestUpdate(t, "v0.1.0", "", &clock{time.Now()})
	if got := u.cachedNotice(); got != "" {
		t.Errorf("nothing cached: got %q", got)
	}
	if err := u.save(updateState{Latest: "v0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if got, want := u.cachedNotice(), updateNotice("v0.2.0", "v0.1.0"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildTag(t *testing.T) {
	cases := []struct {
		name string
		b    buildInfo
		want string
	}{
		{"dev build", buildInfo{commit: "abc", date: "2026-09-10T11:47:21Z"}, ""},
		{"clean tag", buildInfo{version: "v0.1.0", commit: "abc"}, "v0.1.0"},
		{"module install at a tag", buildInfo{version: "v0.1.0"}, "v0.1.0"},
		{"dirty tag", buildInfo{version: "v0.1.0", commit: "abc", modified: true}, ""},
		{"stamped without the v", buildInfo{version: "0.1.0"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.tag(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSkipsUpdateCheck(t *testing.T) {
	cases := map[string]bool{
		"":               true,
		"version":        true,
		"doctor":         true,
		"completion zsh": true,
		"help":           true,
		"__complete":     true,
		"ls":             false,
		"up":             false,
		"artisan":        false,
	}
	for path, want := range cases {
		cmd := rootCmd
		for _, name := range strings.Fields(path) {
			cmd = childNamed(t, cmd, name)
		}
		if got := skipsUpdateCheck(cmd); got != want {
			t.Errorf("slate %s: skip = %v, want %v", path, got, want)
		}
	}
}

func childNamed(t *testing.T, parent *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	c := &cobra.Command{Use: name}
	parent.AddCommand(c)
	t.Cleanup(func() { parent.RemoveCommand(c) })
	return c
}

// Through the root hooks: the notice lands on stderr after a command that
// succeeded; a failed command keeps the answer but gets no notice.
func TestUpdateNoticeFollowsTheCommand(t *testing.T) {
	server := newReleaseServer(t, releaseJSON("v0.2.0"))
	stateDir := t.TempDir()
	orig := updateChecker
	updateChecker = func() *updateCheck {
		return &updateCheck{running: "v0.1.0", stateDir: stateDir, url: server.URL, now: time.Now}
	}
	t.Cleanup(func() { updateChecker = orig })

	var fail bool
	probe := &cobra.Command{
		Use: "update-probe",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "probe ran")
			if fail {
				return errors.New("probe failed")
			}
			return nil
		},
	}
	rootCmd.AddCommand(probe)
	t.Cleanup(func() {
		rootCmd.RemoveCommand(probe)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	run := func() (string, string, error) {
		var stdout, stderr bytes.Buffer
		rootCmd.SetOut(&stdout)
		rootCmd.SetErr(&stderr)
		rootCmd.SetArgs([]string{"update-probe"})
		err := Execute()
		return stdout.String(), stderr.String(), err
	}

	stdout, stderr, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "probe ran\n" {
		t.Errorf("stdout %q", stdout)
	}
	if want := "\n" + updateNotice("v0.2.0", "v0.1.0") + "\n"; stderr != want {
		t.Errorf("stderr %q, want %q", stderr, want)
	}

	fail = true
	if err := os.Remove(filepath.Join(stateDir, "update-check.json")); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := run(); err == nil || strings.Contains(stderr, "is available") {
		t.Errorf("a failed command should get no notice: err %v, stderr %q", err, stderr)
	}
	state := updateChecker().load()
	if state.Latest != "v0.2.0" || !state.NotifiedAt.IsZero() {
		t.Errorf("the answer should be cached with the notice still owed, state %+v", state)
	}
	if pendingUpdate != nil {
		t.Error("the check should not outlive the failed command")
	}
}

func TestReportLatestRelease(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		running string
		want    string
	}{
		{"newer release", releaseJSON("v0.2.0"), "v0.1.0", "v0.2.0 is available: go install github.com/devtime-ltd/slate@latest"},
		{"current", releaseJSON("v0.1.0"), "v0.1.0", "latest release v0.1.0"},
		{"dev build", releaseJSON("v0.1.0"), "", "latest release v0.1.0"},
		{"no release yet", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }, "v0.1.0", "update check failed: github answered 404 Not Found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newReleaseServer(t, tc.handler)
			cache := &updateCheck{running: tc.running, stateDir: t.TempDir(), url: server.URL, now: time.Now}
			var out bytes.Buffer
			reportLatestRelease(&out, server.URL, tc.running, cache)
			if got := out.String(); !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestReportLatestReleaseCachesTheAnswer(t *testing.T) {
	server := newReleaseServer(t, releaseJSON("v0.2.0"))
	cache := &updateCheck{running: "v0.1.0", stateDir: t.TempDir(), url: server.URL, now: time.Now}
	reportLatestRelease(&bytes.Buffer{}, server.URL, "v0.1.0", cache)
	if state := cache.load(); state.Latest != "v0.2.0" || state.CheckedAt.IsZero() {
		t.Errorf("doctor's answer should be cached for the notice, state %+v", state)
	}
	got := runUpdate(cache)
	if n := server.hits.Load(); n != 1 || !strings.Contains(got, "v0.2.0 is available") {
		t.Errorf("the next command should notify from the cache without a fetch: hits %d, got %q", n, got)
	}
}

func TestReportLatestReleaseOptOut(t *testing.T) {
	server := newReleaseServer(t, releaseJSON("v0.2.0"))
	t.Setenv("SLATE_NO_UPDATE_CHECK", "1")
	var out bytes.Buffer
	reportLatestRelease(&out, server.URL, "v0.1.0", nil)
	if out.Len() != 0 || server.hits.Load() != 0 {
		t.Errorf("opted out, yet got %q with %d hits", out.String(), server.hits.Load())
	}
}
