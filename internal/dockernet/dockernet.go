// Package dockernet inspects and reclaims the Docker networks slate creates.
//
// Docker allocates every user-defined bridge network a subnet from its
// default address pools, and the pool count is the real ceiling: stock Docker
// Engine defines 32 (172.17.0.0/12 at /16, 192.168.0.0/16 at /20) and OrbStack
// 30. One workspace takes one network, so a machine running slate hits that
// ceiling long before it runs out of anything else, and compose then fails
// with "all predefined address pools have been fully subnetted".
//
// `slate down` and `slate rm` both remove their network, so a workspace torn
// down through slate costs nothing. The leak is workspaces stopped some other
// way - an OrbStack restart, a reboot, a manual `docker stop`. A stopped
// container detaches its endpoint but the network is a persistent object that
// survives, so it lingers holding a subnet with nothing attached, and no slate
// command ever sees it again.
package dockernet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// projectPrefix mirrors compose.ProjectName, which names every workspace
// "slate__<hostname>"; compose then calls its network "<project>_default".
const projectPrefix = "slate__"

// Idle lists slate workspace networks that hold a subnet with nothing using it.
//
// A network qualifies when compose created it for a slate project, it has no
// attached endpoints, and that project has no container running or restarting. Reclaiming one is safe even
// though a stopped container may still reference it, because compose recreates
// the network on the next `slate up`.
//
// Both halves of that test matter. Endpoints alone are not enough: a stopped
// container detaches, which is exactly the state that strands a subnet, but so
// does a crash-looping one between retries - reclaiming that network mid-restart
// would pull it out from under a container about to come back. Containers alone
// are not enough either, since after a reboot they all still exist, merely
// stopped, and every one of their networks is genuinely idle.
func Idle() ([]string, error) {
	names, err := workspaceNetworks()
	if err != nil || len(names) == 0 {
		return nil, err
	}

	// `docker network inspect` emits \t literally rather than as a tab, so
	// separate with a character that needs no escaping and cannot appear in a
	// network name or a compose project name.
	const format = `{{.Name}}|{{len .Containers}}|{{index .Labels "com.docker.compose.project"}}`
	args := append([]string{"network", "inspect", "--format", format}, names...)
	out, err := runDocker(args...)
	if err != nil {
		return nil, fmt.Errorf("inspecting workspace networks: %w", err)
	}

	busy, err := busyProjects()
	if err != nil {
		return nil, err
	}

	return parseIdle(string(out), busy), nil
}

// busyProjects is the set of compose projects with a container running or
// restarting, whose networks must be left alone.
func busyProjects() (map[string]bool, error) {
	out, err := runDocker("ps",
		"--filter", "status=running",
		"--filter", "status=restarting",
		"--format", `{{.Label "com.docker.compose.project"}}`)
	if err != nil {
		return nil, fmt.Errorf("listing running containers: %w", err)
	}

	busy := map[string]bool{}
	for _, project := range strings.Fields(string(out)) {
		busy[project] = true
	}
	return busy, nil
}

// Remove deletes the named networks, returning those actually removed. A
// network that disappeared between listing and removal, or that a container
// attached to in the meantime, is skipped rather than treated as an error:
// reclaiming is opportunistic and must never be the reason a command fails.
func Remove(names []string) []string {
	var removed []string
	for _, name := range names {
		if exec.Command("docker", "network", "rm", name).Run() == nil {
			removed = append(removed, name)
		}
	}
	return removed
}

// Reclaim removes every idle workspace network and returns those freed. It
// reports no error: every caller is doing this opportunistically alongside
// real work, and a docker hiccup here must not fail that work.
func Reclaim() []string {
	idle, err := Idle()
	if err != nil {
		return nil
	}
	return Remove(idle)
}

// Pools reports how many bridge networks exist against how many the daemon's
// address pools can allocate. Capacity counts IPv4 pools only; each yields
// 2^(size-prefix) subnets, so a pool whose size equals its base prefix (as all
// of OrbStack's /24s do) yields exactly one.
func Pools() (inUse, capacity int, err error) {
	out, err := runDocker("info", "--format", "{{json .DefaultAddressPools}}")
	if err != nil {
		return 0, 0, fmt.Errorf("reading docker address pools: %w", err)
	}

	capacity, err = capacityFromPools(out)
	if err != nil {
		return 0, 0, err
	}

	inUse, err = bridgeNetworkCount()
	if err != nil {
		return 0, capacity, err
	}
	return inUse, capacity, nil
}

func workspaceNetworks() ([]string, error) {
	out, err := runDocker("network", "ls", "--format", "{{.Name}}")
	if err != nil {
		return nil, fmt.Errorf("listing docker networks: %w", err)
	}

	var names []string
	for _, name := range strings.Fields(string(out)) {
		if strings.HasPrefix(name, projectPrefix) {
			names = append(names, name)
		}
	}
	return names, nil
}

func bridgeNetworkCount() (int, error) {
	out, err := runDocker("network", "ls", "--filter", "driver=bridge", "--format", "{{.Name}}")
	if err != nil {
		return 0, fmt.Errorf("listing docker networks: %w", err)
	}
	return len(strings.Fields(string(out))), nil
}

// capacityFromPools sums how many subnets the daemon's IPv4 pools can yield.
func capacityFromPools(raw []byte) (int, error) {
	var pools []struct {
		Base string `json:"Base"`
		Size int    `json:"Size"`
	}
	if err := json.Unmarshal(raw, &pools); err != nil {
		return 0, fmt.Errorf("parsing docker address pools: %w", err)
	}

	capacity := 0
	for _, p := range pools {
		if strings.Contains(p.Base, ":") {
			continue // IPv6 pools are vast and never the binding constraint
		}
		_, mask, ok := strings.Cut(p.Base, "/")
		if !ok {
			continue
		}
		prefix, err := strconv.Atoi(mask)
		if err != nil || prefix < 0 || p.Size < prefix || p.Size > 32 {
			continue // outside what an IPv4 pool can mean; a misparse, not a real config
		}
		capacity += 1 << (p.Size - prefix)
	}
	return capacity, nil
}

// parseIdle picks the reclaimable networks out of pipe-separated
// "<name>|<endpoints>|<project>" lines, skipping any that compose did not
// create for a slate project and any whose project is busy.
func parseIdle(out string, busy map[string]bool) []string {
	var idle []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) < 2 || fields[1] != "0" {
			continue
		}
		// A matching name is not proof slate created it - a hand-made network
		// called "slate__shared" would match too. The compose project label is,
		// and compose always sets it on networks it creates.
		if len(fields) < 3 || !strings.HasPrefix(fields[2], projectPrefix) {
			continue
		}
		if busy[fields[2]] {
			continue
		}
		idle = append(idle, fields[0])
	}
	return idle
}

// runDocker runs a docker command and returns stdout. A failing docker call
// puts its explanation on stderr, which Output() discards into a bare "exit
// status 1", so fold it into the error instead.
func runDocker(args ...string) ([]byte, error) {
	out, err := exec.Command("docker", args...).Output()
	if err == nil {
		return out, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return nil, err
}
