package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Dependency cache info",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Dependency caches live inside each workspace at .slate/composer/ and .slate/npm-cache/.")
		fmt.Println("They are workspace-local and removed automatically with `slate rm`.")
		return nil
	},
}

func init() {
	cacheCmd.GroupID = "tools"
	rootCmd.AddCommand(cacheCmd)
}
