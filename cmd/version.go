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
}

// go derives the main module's version from git: a tag on a clean checkout,
// else a pseudo-version carrying the commit time and hash.
var pseudoVersion = regexp.MustCompile(`[-.](\d{14})-([0-9a-f]{12})`)

func buildVersion() string {
	info, _ := debug.ReadBuildInfo()
	return versionFrom(version, commit, date, info)
}

func versionFrom(v, c, d string, info *debug.BuildInfo) string {
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
	return formatVersion(v, c, d, modified)
}

func pseudoVersionTime(stamp string) string {
	t, err := time.Parse("20060102150405", stamp)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func formatVersion(version, commit, date string, modified bool) string {
	if version == "" {
		version = "dev"
	}
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if modified && commit != "" {
		commit += "-dirty"
	}
	build := strings.TrimSpace(commit + " " + date)
	if build == "" {
		build = "unknown build"
	}
	return version + " (" + build + ")"
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
