package cmd

import (
	"github.com/devtime-ltd/slate/internal/compose"
	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/proxy"
)

func buildServicePorts(env compose.Env, cfg config.ProjectConfig) proxy.ServicePorts {
	services := proxy.ServicePorts{}

	if port, err := compose.Port(env, "app", cfg.AppPort); err == nil && port != "" {
		services[""] = port
	}
	if port, err := compose.Port(env, "vite", cfg.VitePort); err == nil && port != "" {
		services["vite"] = port
	}
	if port, err := compose.Port(env, "mailpit", 8025); err == nil && port != "" {
		services["mailpit"] = port
	}

	return services
}
