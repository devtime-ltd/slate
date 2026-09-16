package cmd

import (
	"fmt"

	"github.com/devtime-ltd/slate/internal/assets"
	"github.com/spf13/cobra"
)

var shellenvScripts = map[string][]byte{
	"zsh":  assets.DevZsh,
	"bash": assets.DevBash,
}

var shellenvCmd = &cobra.Command{
	Use:   "shellenv <zsh|bash>",
	Short: "Print the dev shell function and its completion",
	Long: `Prints shell code defining dev, a function that changes directory to whatever
slate where resolves, with tab completion over project and workspace names.
Add one line to your rc file, after compinit in zsh:

  eval "$(slate shellenv zsh)"    # ~/.zshrc
  eval "$(slate shellenv bash)"   # ~/.bashrc`,
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"zsh", "bash"},
	GroupID:   "tools",
	RunE: func(cmd *cobra.Command, args []string) error {
		script, ok := shellenvScripts[args[0]]
		if !ok {
			return fmt.Errorf("unsupported shell '%s' (zsh or bash)", args[0])
		}
		_, err := cmd.OutOrStdout().Write(script)
		return err
	},
}

func init() {
	rootCmd.AddCommand(shellenvCmd)
}
