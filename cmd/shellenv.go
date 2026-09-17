package cmd

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/devtime-ltd/slate/internal/assets"
	"github.com/spf13/cobra"
)

var shellenvScripts = map[string][]byte{
	"zsh":  assets.DevZsh,
	"bash": assets.DevBash,
}

var shellenvName string

var shellFunctionName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var shellReservedWords = strings.Fields(`case coproc declare do done elif else end esac export fi float for foreach function if in integer nocorrect readonly repeat select then time typeset until while`)

var shellenvScriptCommands = strings.Fields(`cd compadd compctl compdef complete continue false local print printf read return slate true unalias unset`)

func renderShellenv(shell, name string) ([]byte, error) {
	script, ok := shellenvScripts[shell]
	if !ok {
		return nil, fmt.Errorf("unsupported shell '%s' (zsh or bash)", shell)
	}
	if name == "" {
		return nil, fmt.Errorf("--name is required: the shell function to define, e.g. --name dev")
	}
	if !shellFunctionName.MatchString(name) {
		return nil, fmt.Errorf("--name '%s' is not a shell function name (letters, digits and underscores, not starting with a digit)", name)
	}
	if slices.Contains(shellReservedWords, name) {
		return nil, fmt.Errorf("--name '%s' is a shell reserved word", name)
	}
	if slices.Contains(shellenvScriptCommands, name) || strings.HasPrefix(name, "_slate_where") {
		return nil, fmt.Errorf("--name '%s' would shadow a command the printed script runs", name)
	}
	return bytes.ReplaceAll(script, []byte("{{NAME}}"), []byte(name)), nil
}

var shellenvCmd = &cobra.Command{
	Use:   "shellenv <zsh|bash>",
	Short: "Print a shell function that jumps to projects, with completion",
	Long: `Prints shell code defining a function, named by --name, that changes directory
to whatever slate where resolves, with tab completion over project and
workspace names. Add one line to your rc file, after compinit in zsh:

  eval "$(slate shellenv zsh --name dev)"    # ~/.zshrc
  eval "$(slate shellenv bash --name dev)"   # ~/.bashrc`,
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"zsh", "bash"},
	GroupID:   "tools",
	RunE: func(cmd *cobra.Command, args []string) error {
		script, err := renderShellenv(args[0], shellenvName)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(script)
		return err
	},
}

func init() {
	shellenvCmd.Flags().StringVar(&shellenvName, "name", "", "Name of the shell function to define (required)")
	cobra.CheckErr(shellenvCmd.MarkFlagRequired("name"))
	rootCmd.AddCommand(shellenvCmd)
}
