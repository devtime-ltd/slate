package compose

import "github.com/devtime-ltd/slate/internal/workspace"

func NewEnv(name, wsDir string) (Env, error) {
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return Env{}, err
	}
	hostname, err := workspace.Hostname(name)
	if err != nil {
		return Env{}, err
	}
	return Env{
		MainRoot:       mainRoot,
		WorkspaceDir:   wsDir,
		WorkspaceName:  name,
		ComposeProject: "slate__" + hostname,
	}, nil
}
