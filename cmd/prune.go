package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var pruneForce bool

var pruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Delete orphaned workspace branches whose work is pushed or merged",
	Long: `Prune cleans up debris left behind by removed workspaces:

  - stale worktree registrations (directories deleted outside slate rm)
  - slate/* branches with no matching workspace, when safe to delete

A branch is only deleted when its commits are recoverable elsewhere: its tip
matches its upstream (pushed), or it is merged into the default branch.
Anything else is listed with the reason it was kept.

Only branches under the slate/ prefix are considered; workspaces created with
'slate new -b <custom>' must be cleaned up manually.`,
	Args: cobra.NoArgs,
	RunE: runPrune,
}

func init() {
	pruneCmd.Flags().BoolVarP(&pruneForce, "force", "f", false, "Skip confirmation prompt")
	pruneCmd.GroupID = "workspace"
	rootCmd.AddCommand(pruneCmd)
}

type branchStatus struct {
	name   string
	reason string
}

func runPrune(cmd *cobra.Command, args []string) error {
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return err
	}

	// Clear stale registrations first: they pin their branches, which would
	// otherwise show as checked out and be skipped below.
	if err := workspace.PruneWorktrees(); err != nil {
		return err
	}

	branches, err := workspace.BranchesWithPrefix(mainRoot, "slate/")
	if err != nil {
		return err
	}
	wsRoot, err := workspace.WorkspacesRoot()
	if err != nil {
		return err
	}
	inUse := workspace.CheckedOutBranches()

	var deletable, kept []branchStatus
	for _, branch := range branches {
		if inUse[branch] {
			continue
		}
		name := strings.TrimPrefix(branch, "slate/")
		if _, err := os.Stat(filepath.Join(wsRoot, name)); err == nil {
			continue // workspace still exists
		}
		if safe, reason := workspace.BranchSafety(mainRoot, branch); safe {
			deletable = append(deletable, branchStatus{branch, reason})
		} else {
			kept = append(kept, branchStatus{branch, reason})
		}
	}

	if len(deletable) == 0 && len(kept) == 0 {
		fmt.Println(tick() + " no orphaned workspace branches")
		return nil
	}

	if len(kept) > 0 {
		fmt.Println("Keeping (work not recoverable elsewhere):")
		for _, b := range kept {
			fmt.Printf("  %s (%s); delete with `git branch -D %s`\n", b.name, b.reason, b.name)
		}
	}
	if len(deletable) == 0 {
		return nil
	}
	fmt.Println("Orphaned workspace branches safe to delete:")
	for _, b := range deletable {
		fmt.Printf("  %s (%s)\n", b.name, b.reason)
	}

	if !pruneForce {
		fmt.Printf("Delete %d branch(es)? [y/N] ", len(deletable))
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	for _, b := range deletable {
		if err := workspace.DeleteBranch(mainRoot, b.name); err != nil {
			fmt.Printf("  warning: could not delete %s: %v\n", b.name, err)
			continue
		}
		fmt.Printf(""+tick()+" deleted %s\n", b.name)
	}
	return nil
}
