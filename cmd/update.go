package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

const (
	updateCheckEvery   = 24 * time.Hour
	updateRetryAfter   = time.Hour
	updateNotifyEvery  = 24 * time.Hour
	updateFetchTimeout = 2 * time.Second
	updateWaitBudget   = time.Second
	installHint        = "go install github.com/devtime-ltd/slate@latest"
)

var releasesURL = "https://api.github.com/repos/devtime-ltd/slate/releases/latest"

type updateState struct {
	AttemptedAt time.Time `json:"attempted_at,omitzero"`
	CheckedAt   time.Time `json:"checked_at,omitzero"`
	Latest      string    `json:"latest,omitempty"`
	NotifiedAt  time.Time `json:"notified_at,omitzero"`
}

// updateCheck accompanies one command: start begins a fetch when the cached
// answer is stale, finish prints the notice when one is due. A nil check is
// inert, which is how dev builds and opted-out runs behave.
type updateCheck struct {
	running  string
	stateDir string
	url      string
	now      func() time.Time
	started  time.Time
	result   chan string
}

func newUpdateCheck() *updateCheck {
	return updateCheckFor(build().tag())
}

func updateCheckFor(tag string) *updateCheck {
	if tag == "" {
		return nil
	}
	return &updateCheck{running: tag, stateDir: config.DataDir(), url: releasesURL, now: time.Now}
}

// updateChecker is what the root hooks call; tests swap it out.
var updateChecker = func() *updateCheck {
	if !wantsUpdateCheck(os.Getenv, term.IsTerminal(os.Stderr.Fd())) {
		return nil
	}
	return newUpdateCheck()
}

func wantsUpdateCheck(getenv func(string) string, terminal bool) bool {
	return terminal && getenv("SLATE_NO_UPDATE_CHECK") == "" && getenv("SLATE_AGENT") != "1"
}

var noUpdateCheck = map[string]bool{
	"version":                       true,
	"doctor":                        true,
	"completion":                    true,
	"help":                          true,
	cobra.ShellCompRequestCmd:       true,
	cobra.ShellCompNoDescRequestCmd: true,
}

func skipsUpdateCheck(cmd *cobra.Command) bool {
	for cmd.HasParent() && cmd.Parent().HasParent() {
		cmd = cmd.Parent()
	}
	return !cmd.HasParent() || noUpdateCheck[cmd.Name()]
}

func (u *updateCheck) start() {
	if u == nil {
		return
	}
	state := u.load()
	now := u.now()
	if recent(now, state.CheckedAt, updateCheckEvery) || recent(now, state.AttemptedAt, updateRetryAfter) {
		return
	}
	// recorded before the fetch so an abandoned one counts as an attempt
	state.AttemptedAt = now
	if err := u.save(state); err != nil {
		return
	}
	u.started = now
	u.result = make(chan string, 1)
	go func() {
		tag, _ := fetchLatestRelease(u.url)
		u.result <- tag
	}()
}

// finish caches an answer that arrived within the budget and, unless w is
// nil, prints the notice when one is due.
func (u *updateCheck) finish(w io.Writer) {
	if u == nil {
		return
	}
	tag := u.await()
	state := u.load()
	now := u.now()
	changed := false
	// another command may have checked while this one ran
	fresher := state.CheckedAt.After(u.started) && !state.CheckedAt.After(now)
	if tag != "" && !fresher {
		state.CheckedAt = u.started
		state.Latest = tag
		changed = true
	}
	notify := w != nil && newerRelease(state.Latest, u.running) != "" && !recent(now, state.NotifiedAt, updateNotifyEvery)
	if notify {
		state.NotifiedAt = now
		changed = true
	}
	if changed && u.save(state) != nil {
		return
	}
	if notify {
		fmt.Fprintln(w, "\n"+updateNotice(state.Latest, u.running))
	}
}

// await takes a fetch that has answered, and waits for one only while the
// budget lasts. A ready answer is taken before the budget is consulted: a
// select with an expired timer would drop it half the time.
func (u *updateCheck) await() string {
	if u.result == nil {
		return ""
	}
	select {
	case tag := <-u.result:
		return tag
	default:
	}
	wait := updateWaitBudget - u.now().Sub(u.started)
	if wait <= 0 {
		return ""
	}
	select {
	case tag := <-u.result:
		return tag
	case <-time.After(wait):
		return ""
	}
}

// recent is false for a stamp ahead of the clock, so a file written under a
// wrong clock cannot silence the check until real time catches up.
func recent(now, stamp time.Time, within time.Duration) bool {
	return !stamp.After(now) && now.Sub(stamp) < within
}

func updateNotice(latest, running string) string {
	return fmt.Sprintf("slate %s is available (you have %s): %s", latest, running, installHint)
}

func newerRelease(latest, running string) string {
	if !semver.IsValid(latest) || !semver.IsValid(running) || semver.Compare(latest, running) <= 0 {
		return ""
	}
	return latest
}

// cachedNotice answers from the state file alone, for slate version.
func (u *updateCheck) cachedNotice() string {
	if u == nil {
		return ""
	}
	latest := newerRelease(u.load().Latest, u.running)
	if latest == "" {
		return ""
	}
	return updateNotice(latest, u.running)
}

func fetchLatestRelease(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "slate/"+build().String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github answered %s", resp.Status)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("reading the release: %w", err)
	}
	if !semver.IsValid(release.TagName) {
		return "", fmt.Errorf("release tag %q is not a version", release.TagName)
	}
	return release.TagName, nil
}

func (u *updateCheck) statePath() string {
	return filepath.Join(u.stateDir, "update-check.json")
}

func (u *updateCheck) load() updateState {
	var state updateState
	data, err := os.ReadFile(u.statePath())
	if err != nil || json.Unmarshal(data, &state) != nil {
		return updateState{}
	}
	return state
}

func (u *updateCheck) save(state updateState) error {
	if err := os.MkdirAll(u.stateDir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(u.stateDir, "update-check-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), u.statePath())
}

// reportLatestRelease is doctor's line: a live lookup regardless of the
// cooldown, cached afterwards so the notice agrees with it.
func reportLatestRelease(out io.Writer, url, running string, cache *updateCheck) {
	if os.Getenv("SLATE_NO_UPDATE_CHECK") != "" {
		return
	}
	latest, err := fetchLatestRelease(url)
	if err != nil {
		fmt.Fprintf(out, "  "+warn()+" update check failed: %v\n", err)
		return
	}
	if newerRelease(latest, running) != "" {
		fmt.Fprintf(out, "  "+warn()+" %s is available: %s\n", latest, installHint)
	} else {
		fmt.Fprintf(out, "  "+tick()+" latest release %s\n", latest)
	}
	if cache != nil {
		state := cache.load()
		state.CheckedAt = cache.now()
		state.Latest = latest
		cache.save(state)
	}
}
