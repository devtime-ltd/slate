package cmd

import (
	"fmt"
	"os/exec"

	"github.com/devtime-ltd/slate/internal/dockernet"
)

// requireDocker returns an error if the docker CLI is unavailable.
func requireDocker() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found in PATH. Install Docker (or OrbStack on macOS) and try again")
	}
	return nil
}

// networkPoolHint explains an exhausted-address-pool failure, for appending to
// a compose error. Docker reports this as "all predefined address pools have
// been fully subnetted", which names the symptom and gives no hint that the
// cause is old workspaces still holding subnets. Returns "" unless the pools
// really are full, so it never editorialises over an unrelated failure.
func networkPoolHint() string {
	inUse, capacity, err := dockernet.Pools()
	if err != nil || capacity == 0 || inUse < capacity {
		return ""
	}
	return fmt.Sprintf("\n\nDocker has no address pools left (%d of %d networks in use), so it "+
		"cannot create a network for this workspace.\nRun `slate doctor` to see how many are "+
		"reclaimable, or raise the ceiling with default-address-pools in the Docker daemon config.",
		inUse, capacity)
}
