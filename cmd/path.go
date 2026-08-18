package cmd

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var pathOpen bool

var pathCmd = &cobra.Command{
	Use:     "path [workspace]",
	Short:   "Print workspace path (pipeable)",
	Args:    requireWorkspaceName,
	GroupID: "tools",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := resolveWorkspaceArg(args)
		if err != nil {
			return err
		}
		wsDir, err := workspace.WorkspaceDir(name)
		if err != nil {
			return err
		}
		if _, err := os.Stat(wsDir); err != nil {
			return fmt.Errorf("workspace '%s' not found", name)
		}

		if pathOpen {
			opener := "xdg-open"
			if runtime.GOOS == "darwin" {
				opener = "open"
			}
			return hostCommand(opener, wsDir).Run()
		}

		fmt.Println(wsDir)
		return nil
	},
}

var pwdCmd = &cobra.Command{
	Use:     "pwd",
	Short:   "Print the project's main checkout (pipeable)",
	Long:    "Prints the main checkout of the project, resolved from --project or from the CWD.\nThe repo-level counterpart to `slate path`, which names a workspace inside it.",
	Args:    cobra.NoArgs,
	GroupID: "tools",
	RunE: func(cmd *cobra.Command, args []string) error {
		mainRoot, err := workspace.MainRoot()
		if err != nil {
			return err
		}
		fmt.Println(mainRoot)
		return nil
	},
}

var cdCmd = &cobra.Command{
	Use:     "cd [workspace]",
	Short:   "Spawn a sub-shell rooted at the workspace directory",
	Long:    "Spawns $SHELL with cwd set to the workspace dir. Type `exit` to return to the original shell.\nWorks across projects via --project. A shell builtin cd cannot change the parent shell's cwd; wrap this in a shell function if you want that behaviour.",
	Args:    requireWorkspaceName,
	GroupID: "tools",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := resolveWorkspaceArg(args)
		if err != nil {
			return err
		}
		wsDir, err := workspace.WorkspaceDir(name)
		if err != nil {
			return err
		}
		if _, err := os.Stat(wsDir); err != nil {
			return fmt.Errorf("workspace '%s' not found", name)
		}
		return spawnShellAt(wsDir)
	},
}

var codeCmd = &cobra.Command{
	Use:     "code [workspace]",
	Short:   "Open workspace in your editor",
	Args:    requireWorkspaceName,
	GroupID: "tools",
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := resolveWorkspaceArg(args)
		if err != nil {
			return err
		}
		wsDir, err := workspace.WorkspaceDir(name)
		if err != nil {
			return err
		}
		if _, err := os.Stat(wsDir); err != nil {
			return fmt.Errorf("workspace '%s' not found", name)
		}

		editor, err := resolveEditor()
		if err != nil {
			return err
		}

		return hostCommand(editor, wsDir).Run()
	},
}

// resolveEditor returns the editor to use, prompting and saving on first run.
// Order: project slate.yml > global config > $VISUAL > $EDITOR > prompt.
func resolveEditor() (string, error) {
	if mainRoot, err := workspace.MainRoot(); err == nil {
		if cfg, err := config.LoadProject(mainRoot); err == nil && cfg.Editor != "" {
			return cfg.Editor, nil
		}
	}

	globalCfg, _ := config.LoadGlobal()
	if globalCfg.Editor != "" {
		return globalCfg.Editor, nil
	}

	if v := os.Getenv("VISUAL"); v != "" {
		return v, nil
	}
	if v := os.Getenv("EDITOR"); v != "" && v != "nano" && v != "vi" {
		return v, nil
	}

	return promptForEditor()
}

func promptForEditor() (string, error) {
	fmt.Println("No editor configured for slate.")
	fmt.Println("Examples: code (VS Code), cursor, zed, subl (Sublime), nvim, idea")
	fmt.Print("Editor command: ")

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	editor := strings.TrimSpace(answer)
	if editor == "" {
		return "", fmt.Errorf("no editor provided")
	}

	cfg, _ := config.LoadGlobal()
	cfg.Editor = editor
	if err := config.SaveGlobal(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save editor preference: %v\n", err)
	} else {
		fmt.Printf("Saved %q as your editor (in ~/.config/slate/config.yml).\n", editor)
		fmt.Println("Tip: override per-project by adding `editor: <command>` to your slate.yml.")
	}
	return editor, nil
}

func init() {
	pathCmd.Flags().BoolVar(&pathOpen, "open", false, "Open workspace directory in file manager")
	rootCmd.AddCommand(pathCmd)
	rootCmd.AddCommand(pwdCmd)
	rootCmd.AddCommand(codeCmd)
	rootCmd.AddCommand(cdCmd)
}
