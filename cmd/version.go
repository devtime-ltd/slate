package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

// Set with -ldflags "-X github.com/devtime-ltd/slate/cmd.version=v1.2.3"
// (likewise commit and date); otherwise the build info go embeds is used.
var (
	version string
	commit  string
	date    string
)

var versionCmd = &cobra.Command{
	Use:     "version",
	Short:   "Print the build version, commit and date",
	GroupID: "tools",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		printVersion(cmd)
	},
}

func printVersion(cmd *cobra.Command) {
	fmt.Fprintln(cmd.OutOrStdout(), "slate "+buildVersion())
	if others := otherSlatesOnPath(); len(others) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "  warning: other slate binaries on PATH: %s\n", strings.Join(others, ", "))
	}
	if notice := updateChecker().cachedNotice(); notice != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "  "+notice)
	}
}

// go derives the main module's version from git: a tag on a clean checkout,
// else a pseudo-version carrying the commit time and hash.
var pseudoVersion = regexp.MustCompile(`[-.](\d{14})-([0-9a-f]{12})`)

type buildInfo struct {
	version, commit, date string
	modified              bool
}

func build() buildInfo {
	info, _ := debug.ReadBuildInfo()
	return buildFrom(version, commit, date, info)
}

func buildVersion() string {
	return build().String()
}

func buildFrom(v, c, d string, info *debug.BuildInfo) buildInfo {
	modified := false
	if info != nil {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if c == "" {
					c = s.Value
				}
			case "vcs.time":
				if d == "" {
					d = s.Value
				}
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		main := strings.TrimSuffix(info.Main.Version, "+dirty")
		// a module-mode install (go install ...@latest) has no vcs stamp at all
		if m := pseudoVersion.FindStringSubmatch(main); m != nil {
			if c == "" {
				c = m[2]
			}
			if d == "" {
				d = pseudoVersionTime(m[1])
			}
		} else if v == "" && main != "(devel)" {
			v = main
		}
	}
	return buildInfo{version: v, commit: c, date: d, modified: modified}
}

func pseudoVersionTime(stamp string) string {
	t, err := time.Parse("20060102150405", stamp)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// tag is the release this binary is, or "" for anything a release check
// could not compare: an untagged or dirty checkout, a version stamped
// without the v prefix.
func (b buildInfo) tag() string {
	if b.modified || !semver.IsValid(b.version) {
		return ""
	}
	return b.version
}

func (b buildInfo) String() string {
	commit := b.commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if b.modified && commit != "" {
		commit += "-dirty"
	}
	stamp := strings.TrimSpace(commit + " " + b.date)
	switch {
	case b.version == "" && stamp == "":
		return "dev (unknown build)"
	case b.version == "":
		return "dev (" + stamp + ")"
	case stamp == "":
		return b.version
	}
	return b.version + " (" + stamp + ")"
}

// otherSlatesOnPath lists the slate executables on PATH that are not the
// running binary, however many links or PATH entries reach each of them.
// os.Executable may already have followed a symlink, so the name is fixed.
func otherSlatesOnPath() []string {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	return otherExecutables("slate", self, filepath.SplitList(os.Getenv("PATH")))
}

func otherExecutables(name, self string, dirs []string) []string {
	selfInfo, err := os.Stat(self)
	if err != nil {
		return nil
	}
	seen := []os.FileInfo{selfInfo}
	var others []string
	for _, dir := range dirs {
		candidate, err := filepath.Abs(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		if slices.ContainsFunc(seen, func(s os.FileInfo) bool { return os.SameFile(s, info) }) {
			continue
		}
		seen = append(seen, info)
		others = append(others, candidate)
	}
	return others
}

func init() {
	rootCmd.Flags().BoolP("version", "v", false, "Print the build version, commit and date")
	rootCmd.Run = func(cmd *cobra.Command, args []string) {
		if v, _ := cmd.Flags().GetBool("version"); v {
			printVersion(cmd)
			return
		}
		cmd.Help()
	}
	rootCmd.AddCommand(versionCmd)
}
