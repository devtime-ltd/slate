package cmd

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/dockernet"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check that all dependencies are in place",
	RunE:  runDoctor,
}

func runDoctor(cmd *cobra.Command, args []string) error {
	fmt.Println("Checking slate dependencies...")
	allOK := true

	check := func(ok bool, label string) {
		if ok {
			fmt.Printf("  "+tick()+" %s\n", label)
		} else {
			fmt.Printf("  "+cross()+" %s\n", label)
			allOK = false
		}
	}

	checkWarn := func(ok bool, label string) {
		if ok {
			fmt.Printf("  "+tick()+" %s\n", label)
		} else {
			fmt.Printf("  "+warn()+" %s\n", label)
		}
	}

	// Docker
	if v := cmdVersion("docker", "--version"); v != "" {
		check(true, fmt.Sprintf("docker (%s)", v))
	} else {
		check(false, "docker not found")
	}

	dockerUp := exec.Command("docker", "info").Run() == nil
	check(dockerUp, "docker daemon running")

	if dockerUp {
		reportNetworkPools(checkWarn)
	}

	// HTTPS proxy (slate container > standalone Caddy > Herd)
	proxyFound := false
	caddyAPIReady := false

	client := http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get("http://127.0.0.1:2019/config/"); err == nil {
		resp.Body.Close()
		caddyAPIReady = true
	}

	if isProxyRunning() {
		fmt.Println("  " + tick() + " slate proxy (Caddy container, will be used)")
		proxyFound = true
	} else if caddyAPIReady {
		fmt.Println("  " + tick() + " caddy (API reachable, will be used)")
		proxyFound = true
	}
	if v := cmdVersion("herd", "--version"); v != "" {
		label := fmt.Sprintf("herd (%s)", v)
		if proxyFound {
			label += " (available, Caddy preferred)"
		} else {
			label += " (will be used)"
			proxyFound = true
		}
		fmt.Printf("  "+tick()+" %s\n", label)
	}
	if !proxyFound {
		check(false, "no HTTPS proxy. Run `slate proxy start` or install Caddy/Herd.")
	}

	// Git
	if v := cmdVersion("git", "--version"); v != "" {
		check(true, fmt.Sprintf("git (%s)", v))
	} else {
		check(false, "git not found")
	}

	// tmux
	if v := cmdVersion("tmux", "-V"); v != "" {
		checkWarn(true, fmt.Sprintf("tmux (%s)", v))
	} else {
		checkWarn(false, "tmux not found (needed for slate attach)")
	}

	// DNS
	out, err := exec.Command("dig", "+short", "test.test", "@127.0.0.1").Output()
	dnsOK := err == nil && strings.Contains(string(out), "127.0.0.1")
	check(dnsOK, "*.test DNS resolves to 127.0.0.1")
	if !dnsOK {
		fmt.Println("     Run `slate dns start` (or `slate setup`).")
	}

	// Entrypoint
	entrypoint := config.DataDir() + "/entrypoint.sh"
	_, err = os.Stat(entrypoint)
	check(err == nil, fmt.Sprintf("slate entrypoint at %s", entrypoint))

	// HTTPS port: report the port the proxy is actually serving on (which can
	// differ from config when the configured port was taken, e.g. Herd on 443),
	// falling back to the configured value when the proxy isn't running.
	_, httpsPort, _ := DetectProxyPorts()
	fmt.Printf("\n  HTTPS port: %d\n", httpsPort)

	fmt.Println()
	if allOK {
		fmt.Println("All good.")
	} else {
		fmt.Println("Some checks failed.")
	}
	return nil
}

func cmdVersion(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	// Extract version-like substring
	for _, word := range strings.Fields(v) {
		if len(word) > 0 && (word[0] >= '0' && word[0] <= '9' || word[0] == 'v') {
			return strings.TrimRight(word, ",")
		}
	}
	return v
}

// lowNetworkHeadroom is how few creatable networks counts as worth warning
// about. Every workspace needs one, so running this close means the next
// `slate new` or two is likely to fail.
const lowNetworkHeadroom = 3

// reportNetworkPools surfaces Docker's address-pool budget, which is the real
// ceiling on concurrent workspaces and is invisible until it fails: stock
// Docker Engine allows 32 networks, OrbStack 30. Idle ones are counted as
// headroom because `slate down` and a failed `slate up` both reclaim them.
func reportNetworkPools(checkWarn func(bool, string)) {
	inUse, capacity, err := dockernet.Pools()
	if err != nil || capacity == 0 {
		return
	}

	idle, _ := dockernet.Idle()
	label := fmt.Sprintf("docker address pools (%d of %d networks in use", inUse, capacity)
	if len(idle) > 0 {
		label += fmt.Sprintf(", %d reclaimable", len(idle))
	}
	label += ")"

	free := capacity - inUse + len(idle)
	checkWarn(free >= lowNetworkHeadroom, label)

	if free < lowNetworkHeadroom {
		fmt.Println("     Nearly out of networks; the next `slate new` may fail to start.")
		fmt.Println("     Raise the ceiling with default-address-pools in the Docker daemon config.")
	}
	if len(idle) > 0 {
		fmt.Printf("     %d idle %s can be reclaimed: `slate down` sweeps them, or `docker network prune`.\n",
			len(idle), plural(len(idle), "network", "networks"))
	}
}
