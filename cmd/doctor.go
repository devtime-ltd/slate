package cmd

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/devtime-ltd/slate/internal/config"
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
			fmt.Printf("  ✅ %s\n", label)
		} else {
			fmt.Printf("  ❌ %s\n", label)
			allOK = false
		}
	}

	warn := func(ok bool, label string) {
		if ok {
			fmt.Printf("  ✅ %s\n", label)
		} else {
			fmt.Printf("  ⚠️  %s\n", label)
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

	// HTTPS proxy (slate container > standalone Caddy > Herd)
	proxyFound := false
	caddyAPIReady := false

	client := http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get("http://127.0.0.1:2019/config/"); err == nil {
		resp.Body.Close()
		caddyAPIReady = true
	}

	if isSlateProxyRunning() {
		fmt.Printf("  ✅ slate proxy (Caddy container, will be used)\n")
		proxyFound = true
	} else if caddyAPIReady {
		fmt.Printf("  ✅ caddy (API reachable, will be used)\n")
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
		fmt.Printf("  ✅ %s\n", label)
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
		warn(true, fmt.Sprintf("tmux (%s)", v))
	} else {
		warn(false, "tmux not found (needed for slate attach)")
	}

	// DNS
	out, err := exec.Command("dig", "+short", "test.test", "@127.0.0.1").Output()
	dnsOK := err == nil && strings.Contains(string(out), "127.0.0.1")
	check(dnsOK, "*.test DNS resolves to 127.0.0.1")

	// Entrypoint
	entrypoint := config.DataDir() + "/entrypoint.sh"
	_, err = os.Stat(entrypoint)
	check(err == nil, fmt.Sprintf("slate entrypoint at %s", entrypoint))

	// Global config
	cfg, _ := config.LoadGlobal()
	fmt.Printf("\n  HTTPS port: %d\n", cfg.HTTPSPort)
	fmt.Printf("  Proxy preference: %s\n", cfg.Proxy)

	fmt.Println()
	if allOK {
		fmt.Println("All good.")
	} else {
		fmt.Println("Some checks failed.")
	}
	return nil
}

func isSlateProxyRunning() bool {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", "slate-proxy").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
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
