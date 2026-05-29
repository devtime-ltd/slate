package cmd

import (
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
)

// resolveProject returns the project name honouring slate.yml's project: override.
func resolveProject() (string, error) {
	mainRoot, err := workspace.MainRoot()
	if err != nil {
		return "", err
	}
	cfg, _ := config.LoadProject(mainRoot)
	return workspace.ProjectName(cfg.Project)
}

// resolveHostname returns the {project}--{workspace} hostname, honouring overrides.
func resolveHostname(name string) (string, error) {
	project, err := resolveProject()
	if err != nil {
		return "", err
	}
	return workspace.HostnameForProject(project, name), nil
}
