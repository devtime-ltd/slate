package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func SaveGlobal(cfg GlobalConfig) error {
	dir := GlobalConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "config.yml"), data, 0o644)
}
