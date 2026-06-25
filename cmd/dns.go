package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

const dnsContainerName = "slate-dns"
const dnsImage = "alpine:3"
const resolverPath = "/etc/resolver/test"

var dnsCmd = &cobra.Command{
	Use:   "dns",
	Short: "Manage the *.test DNS resolver",
	Long:  "Runs a dnsmasq container that resolves *.test to 127.0.0.1 and wires it into the system resolver.",
}

var dnsStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the DNS resolver and install the system resolver entry",
	RunE:  func(cmd *cobra.Command, args []string) error { return setupDNS() },
}

var dnsStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the DNS resolver and remove the system resolver entry",
	RunE:  func(cmd *cobra.Command, args []string) error { return teardownDNS() },
}

var dnsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check DNS resolver status",
	RunE:  runDNSStatus,
}

func init() {
	dnsCmd.AddCommand(dnsStartCmd, dnsStopCmd, dnsStatusCmd)
	dnsCmd.GroupID = "tools"
	rootCmd.AddCommand(dnsCmd)
}

// setupDNS makes *.test resolve to 127.0.0.1: a dnsmasq container on
// 127.0.0.1:53, plus (on macOS) an /etc/resolver/test entry pointing at it. If
// resolution already works — e.g. the user runs their own dnsmasq — it leaves
// everything untouched.
func setupDNS() error {
	if dnsResolves() {
		fmt.Println("  " + tick() + " *.test DNS already resolves")
		return nil
	}

	if err := startDNSContainer(); err != nil {
		return err
	}

	if runtime.GOOS != "darwin" {
		fmt.Println("  " + warn() + " DNS resolver running on 127.0.0.1:53.")
		fmt.Println("  Point your system resolver for *.test at 127.0.0.1 (systemd-resolved/NetworkManager).")
		return nil
	}

	if err := installResolver(); err != nil {
		fmt.Printf("  "+warn()+" could not install %s: %v\n", resolverPath, err)
		fmt.Printf("  Run manually: sudo mkdir -p /etc/resolver && echo 'nameserver 127.0.0.1' | sudo tee %s\n", resolverPath)
		return nil
	}

	if dnsResolves() {
		fmt.Println("  " + tick() + " *.test DNS resolves to 127.0.0.1")
	} else {
		fmt.Println("  " + tick() + " DNS configured (resolution may take a moment to take effect)")
	}
	return nil
}

func teardownDNS() error {
	exec.Command("docker", "rm", "-f", dnsContainerName).Run()
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(resolverPath); err == nil {
			rm := exec.Command("sudo", "rm", "-f", resolverPath)
			rm.Stdin, rm.Stdout, rm.Stderr = os.Stdin, os.Stdout, os.Stderr
			rm.Run()
		}
	}
	fmt.Println("  " + tick() + " DNS resolver removed")
	return nil
}

func startDNSContainer() error {
	if isDNSRunning() {
		return nil
	}
	if proc := portUser(53); proc != "" {
		fmt.Printf("  "+warn()+" Port 53 is in use by %s; skipping DNS container.\n", proc)
		return fmt.Errorf("port 53 already in use")
	}

	exec.Command("docker", "rm", "-f", dnsContainerName).Run()
	fmt.Println("Starting DNS resolver (127.0.0.1:53)...")

	// alpine + dnsmasq installed at boot, so slate depends on Docker alone rather
	// than a third-party dnsmasq image. `command -v` skips the install on restart
	// (the writable layer persists), so a reboot doesn't need network.
	run := exec.Command("docker", "run", "-d",
		"--name", dnsContainerName,
		"--restart", "unless-stopped",
		"-p", "127.0.0.1:53:53/udp",
		"-p", "127.0.0.1:53:53/tcp",
		dnsImage, "sh", "-c",
		"command -v dnsmasq >/dev/null || apk add --no-cache dnsmasq >/dev/null; exec dnsmasq -k --no-resolv --no-hosts --address=/test/127.0.0.1",
	)
	run.Stdout, run.Stderr = os.Stdout, os.Stderr
	if err := run.Run(); err != nil {
		return fmt.Errorf("failed to start DNS container: %w", err)
	}
	return nil
}

func installResolver() error {
	fmt.Printf("Installing %s (needs sudo)...\n", resolverPath)
	c := exec.Command("sudo", "sh", "-c",
		"mkdir -p /etc/resolver && printf 'nameserver 127.0.0.1\\n' > "+resolverPath)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

func runDNSStatus(cmd *cobra.Command, args []string) error {
	if isDNSRunning() {
		fmt.Println(tick() + " DNS resolver running (127.0.0.1:53)")
	} else {
		fmt.Println(cross() + " DNS resolver not running")
	}
	if dnsResolves() {
		fmt.Println("   *.test resolves to 127.0.0.1")
	} else {
		fmt.Println("   *.test does NOT resolve")
	}
	return nil
}

func isDNSRunning() bool {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", dnsContainerName).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func dnsResolves() bool {
	out, err := exec.Command("dig", "+short", "+time=2", "+tries=1", "test.test", "@127.0.0.1").Output()
	return err == nil && strings.Contains(string(out), "127.0.0.1")
}

// portUser returns the name of the process listening on a port, or "" if free.
func portUser(port int) string {
	out, _ := exec.Command("lsof", "-nP", fmt.Sprintf("-i:%d", port)).Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return ""
	}
	if parts := strings.Fields(lines[1]); len(parts) > 0 {
		return parts[0]
	}
	return ""
}
