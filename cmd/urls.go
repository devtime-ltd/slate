package cmd

import (
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var urlsCmd = &cobra.Command{
	Use:   "urls [workspace]",
	Short: "Print a workspace's URLs (pipeable)",
	Long: `Prints the workspace's main URL, then the URL of each running web service
(such as vite, mailpit or the scaffold's subdomains) and host:port for mysql
and postgres.

Piped, each line is a label and a URL separated by a tab, the main URL
first and labelled main.`,
	Args:    requireWorkspaceName,
	GroupID: "tools",
	RunE:    runURLs,
}

func init() {
	rootCmd.AddCommand(urlsCmd)
}

func runURLs(cmd *cobra.Command, args []string) error {
	name, wsDir, err := resolveNameOrCwd(args)
	if err != nil {
		return err
	}
	hostname, err := resolveHostname(name)
	if err != nil {
		return err
	}
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return err
	}
	cfg, err := config.LoadProjectForWorkspace(mainRoot, wsDir)
	if err != nil {
		return err
	}
	env, err := compose.NewEnv(name, wsDir, hostname)
	if err != nil {
		return err
	}
	proxyConfig, _ := loadProxyConfig(false)

	live, err := compose.LiveServices(env)
	if err != nil {
		return err
	}
	if len(live) == 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "  note: %s is not running; `slate up` starts it\n", name)
	}
	portFor := livePortFor(live, composePortFor(env))

	out := cmd.OutOrStdout()
	if !isTerminal(out) {
		main, links := workspaceLinks(portFor, hostname, cfg, proxyConfig)
		printLinks(out, main, links)
		return nil
	}
	fmt.Fprintln(out, urlBlock(workspaceURLs(portFor, hostname, cfg, proxyConfig)))
	return nil
}

// livePortFor skips services that are not running: a container stopped
// outside slate can still report its last published port.
func livePortFor(live []string, portFor func(string, int) string) func(string, int) string {
	return func(service string, containerPort int) string {
		if !slices.Contains(live, service) {
			return ""
		}
		return portFor(service, containerPort)
	}
}

func printLinks(w io.Writer, main string, links []serviceLink) {
	fmt.Fprintf(w, "main\t%s\n", main)
	for _, l := range links {
		fmt.Fprintf(w, "%s\t%s\n", l.label, l.url)
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
