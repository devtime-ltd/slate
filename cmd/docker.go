package cmd

import (
	"fmt"
	"os/exec"
)

// requireDocker returns an error if the docker CLI is unavailable.
func requireDocker() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found in PATH. Install Docker (or OrbStack on macOS) and try again")
	}
	return nil
}
