package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [scaffold]",
	Short: "Create a slate.yml for this project",
	Long: `Generates a slate.yml config file in the current directory.
The scaffold determines what Docker infrastructure slate generates at
runtime (Dockerfile, compose.yaml, etc). Available scaffolds: laravel,
nextjs. "slate init inline" writes a starter inline scaffold instead,
where the project commits its own compose file (see README).

No other files are created. Docker files are generated on the fly
by slate new / slate up into a .slate/ directory (gitignored).`,
	Args: cobra.MaximumNArgs(1),
	RunE: runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	tmpl := "laravel"
	if len(args) > 0 {
		tmpl = args[0]
	}

	switch tmpl {
	case "laravel", "nextjs", "inline":
	default:
		return fmt.Errorf("unknown scaffold: %s (available: laravel, nextjs; or run `slate init inline` for a bring-your-own-compose template)", tmpl)
	}

	if _, err := workspace.MainRoot(); err != nil {
		return fmt.Errorf("not inside a git repo. Run `git init` first")
	}

	cwd, _ := os.Getwd()
	dest := filepath.Join(cwd, "slate.yml")

	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("slate.yml already exists")
	}

	content := generateSlateYml(tmpl)

	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		return err
	}

	if mainRoot, err := workspace.MainRoot(); err == nil {
		config.RegisterProject(mainRoot)
	}

	fmt.Println("Created slate.yml")
	fmt.Println("\nNext: slate new <feature-name>")

	return nil
}

func generateSlateYml(scaffold string) string {
	switch scaffold {
	case "laravel":
		return `scaffold: laravel

# Override lifecycle (optional, scaffold provides sensible defaults):
# setup: |   # runs in the container on every slate new / slate up
# fresh: |   # runs after setup on slate new and slate up --fresh

# apt_packages: [ghostscript, imagemagick, libmagickwand-dev]
# php_extensions: [imagick]
`
	case "nextjs":
		return `scaffold: nextjs

# Override lifecycle (optional, scaffold provides sensible defaults):
# setup: |   # runs in the container on every slate new / slate up
# fresh: |   # runs after setup on slate new and slate up --fresh
`
	case "inline":
		return `# Inline scaffold: bring your own compose file (committed to this repo).
# A .tmpl extension opts into template rendering; see the slate README.
scaffold:
  compose: ./slate/compose.yaml
  subdomains:
    "@": { service: app, port: 8080 }   # "@" = the main hostname (DNS-style apex)
#     vite: { service: vite, port: 5173 }
#   vars:      # extra data for a compose .tmpl ({{.Vars.key}})
#     key: value

# setup: |   # runs in the app container on every slate new / slate up
# fresh: |   # runs after setup on slate new and slate up --fresh

# tools:
#   npm: { service: app, command: [npm] }
`
	default:
		return fmt.Sprintf("scaffold: %s\n", scaffold)
	}
}
