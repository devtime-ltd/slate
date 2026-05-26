package assets

import (
	_ "embed"
	"os"
	"path/filepath"

	"github.com/devtime-ltd/slate/internal/config"
)

//go:embed entrypoint.sh
var Entrypoint []byte

func EnsureEntrypoint() (string, error) {
	dir := config.DataDir()
	path := filepath.Join(dir, "entrypoint.sh")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	// Always overwrite: keeps it in sync with the binary version.
	if err := os.WriteFile(path, Entrypoint, 0o755); err != nil {
		return "", err
	}

	return path, nil
}
