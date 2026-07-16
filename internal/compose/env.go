package compose

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
)

func ProjectName(hostname string) string {
	return "slate__" + hostname
}

func NewEnv(name, wsDir, hostname string) (Env, error) {
	entrypoint := filepath.Join(config.DataDir(), "entrypoint.sh")
	if _, err := os.Stat(entrypoint); err != nil {
		return Env{}, fmt.Errorf("slate entrypoint missing. Run `slate setup` first")
	}

	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return Env{}, err
	}
	return Env{
		MainRoot:       mainRoot,
		WorkspaceDir:   wsDir,
		WorkspaceName:  name,
		ComposeProject: ProjectName(hostname),
	}, nil
}
