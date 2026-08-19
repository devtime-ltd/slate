package cmd

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/proxy"
	"github.com/spf13/cobra"
)

// Single source of truth lives in internal/proxy, which needs the name too
// to decide whether slate may replace a running Caddy config.
const proxyContainerName = proxy.ProxyContainerName
const proxyImage = "caddy:2.10"

var proxyCmd = &cobra.Command{
	Use:   "proxy",
	Short: "Manage the slate HTTPS proxy",
	Long:  "Starts/stops a Caddy container that handles HTTPS termination for all workspaces.",
}

var proxyPort int
var proxyHTTPPort int
var proxyNoTLS bool

var proxyStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the proxy",
	RunE:  runProxyStart,
}

var proxyStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the HTTPS proxy",
	RunE:  runProxyStop,
}

var proxyStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check proxy status",
	RunE:  runProxyStatus,
}

var proxyTrustCmd = &cobra.Command{
	Use:   "trust",
	Short: "Install the proxy's CA certificate into the system trust store",
	RunE:  runProxyTrust,
}

func init() {
	proxyStartCmd.Flags().IntVar(&proxyPort, "port", 0, "HTTPS port (default: from config or 443)")
	proxyStartCmd.Flags().IntVar(&proxyHTTPPort, "http-port", 0, "HTTP port (default: from config or 80)")
	proxyStartCmd.Flags().BoolVar(&proxyNoTLS, "no-tls", false, "HTTP only, no HTTPS")
	proxyCmd.AddCommand(proxyStartCmd)
	proxyCmd.AddCommand(proxyStopCmd)
	proxyCmd.AddCommand(proxyStatusCmd)
	proxyCmd.AddCommand(proxyTrustCmd)
	proxyCmd.GroupID = "tools"
	rootCmd.AddCommand(proxyCmd)
}

func runProxyStart(cmd *cobra.Command, args []string) error {
	if isProxyRunning() {
		fmt.Println("Proxy is already running.")
		return nil
	}

	cfg, _ := config.LoadGlobal()

	httpPort := proxyHTTPPort
	if httpPort == 0 {
		httpPort = cfg.HTTPPort
	}
	if httpPort == 0 {
		httpPort = 80
	}

	httpsPort := proxyPort
	if httpsPort == 0 {
		httpsPort = cfg.HTTPSPort
	}
	if httpsPort == 0 {
		httpsPort = 443
	}

	useTLS := !proxyNoTLS && cfg.TLS

	// Check for port conflicts
	portsToCheck := []int{httpPort}
	if useTLS {
		portsToCheck = append(portsToCheck, httpsPort)
	}
	for _, p := range portsToCheck {
		if out, _ := exec.Command("lsof", "-i", fmt.Sprintf(":%d", p), "-P", "-n", "-sTCP:LISTEN").Output(); len(out) > 0 {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) > 1 {
				parts := strings.Fields(lines[1])
				process := "unknown"
				if len(parts) > 0 {
					process = parts[0]
				}
				fmt.Printf(""+warn()+" Port %d is in use by %s.\n", p, process)
				fmt.Printf("   Stop it first, or configure ports in ~/.config/slate/config.yml\n")
				return fmt.Errorf("port %d is already in use", p)
			}
		}
	}

	// Remove any stopped container with the same name
	exec.Command("docker", "rm", "-f", proxyContainerName).Run()

	var caddyfile string
	if useTLS {
		fmt.Printf("Starting proxy (HTTP %d, HTTPS %d)...\n", httpPort, httpsPort)
		caddyfile = `{
	admin 0.0.0.0:2019
	local_certs
}
`
	} else {
		fmt.Printf("Starting proxy (HTTP %d, no TLS)...\n", httpPort)
		caddyfile = `{
	admin 0.0.0.0:2019
	auto_https off
}
`
	}

	runArgs := []string{
		"run", "-d",
		"--name", proxyContainerName,
		"--restart", "unless-stopped",
		"--add-host", "host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("%d:80", httpPort),
		"-p", "127.0.0.1:2019:2019",
		"-v", "slate-proxy-data:/data",
		"-v", "slate-proxy-config:/config",
	}
	if useTLS {
		runArgs = append(runArgs, "-p", fmt.Sprintf("%d:443", httpsPort))
	}
	// --resume makes caddy prefer its autosaved config (persisted in the
	// slate-proxy-config volume) over the seed Caddyfile, so API-registered
	// routes survive container restarts and host reboots.
	runArgs = append(runArgs,
		proxyImage,
		"sh", "-c",
		fmt.Sprintf(`echo '%s' > /etc/caddy/Caddyfile && caddy run --resume --config /etc/caddy/Caddyfile --adapter caddyfile`, caddyfile),
	)

	dockerCmd := exec.Command("docker", runArgs...)
	dockerCmd.Stdout = os.Stdout
	dockerCmd.Stderr = os.Stderr
	if err := dockerCmd.Run(); err != nil {
		return fmt.Errorf("failed to start proxy: %w", err)
	}

	// Wait for admin API, then initialize the server config
	fmt.Print("Waiting for proxy to be ready...")
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		if isProxyAPIReady() {
			fmt.Println(" ready.")
			if err := proxy.EnsureServer(useTLS); err != nil {
				fmt.Printf("  warning: could not initialize proxy config: %v\n", err)
			}

			if useTLS {
				fmt.Printf("\n"+tick()+" Proxy running (HTTP %d, HTTPS %d)\n", httpPort, httpsPort)
				fmt.Println("   Run `slate proxy trust` to install the CA certificate.")
			} else {
				fmt.Printf("\n"+tick()+" Proxy running (HTTP %d)\n", httpPort)
			}
			fmt.Println("   Admin API: http://127.0.0.1:2019")
			return nil
		}
		fmt.Print(".")
	}

	return fmt.Errorf("proxy started but admin API not responding")
}

func runProxyStop(cmd *cobra.Command, args []string) error {
	if !isProxyRunning() {
		fmt.Println("Proxy is not running.")
		return nil
	}

	fmt.Println("Stopping proxy...")
	exec.Command("docker", "stop", proxyContainerName).Run()
	exec.Command("docker", "rm", proxyContainerName).Run()
	fmt.Println("" + tick() + " Proxy stopped")
	return nil
}

func runProxyStatus(cmd *cobra.Command, args []string) error {
	if isProxyRunning() {
		fmt.Println("" + tick() + " Proxy is running")
		if isProxyAPIReady() {
			fmt.Println("   Admin API: reachable at http://127.0.0.1:2019")
		}

	} else {
		fmt.Println("" + cross() + " Proxy is not running")
		fmt.Println("   Run `slate proxy start` to start it.")
	}
	return nil
}

func runProxyTrust(cmd *cobra.Command, args []string) error {
	if !isProxyRunning() {
		return fmt.Errorf("proxy is not running. Run `slate proxy start` first")
	}

	fmt.Println("Extracting CA certificate from proxy...")

	// Copy the CA cert from the Caddy container
	dataDir := config.DataDir()
	os.MkdirAll(dataDir, 0o755)
	certPath := dataDir + "/slate-ca.crt"

	copyCmd := exec.Command("docker", "cp",
		proxyContainerName+":/data/caddy/pki/authorities/local/root.crt",
		certPath)
	if err := copyCmd.Run(); err != nil {
		return fmt.Errorf("could not extract CA cert: %w (proxy may need a moment to generate it, try again)", err)
	}

	fmt.Printf("CA certificate saved to %s\n", certPath)
	fmt.Println("Installing into system trust store...")

	// Platform-specific trust installation
	var trustCmd *exec.Cmd
	if _, err := os.Stat("/usr/bin/security"); err == nil {
		// macOS
		trustCmd = exec.Command("sudo", "security", "add-trusted-cert",
			"-d", "-r", "trustRoot",
			"-k", "/Library/Keychains/System.keychain",
			certPath)
	} else {
		// Linux (Debian/Ubuntu)
		trustCmd = exec.Command("sudo", "cp", certPath, "/usr/local/share/ca-certificates/slate-ca.crt")
		fmt.Println("After this, run: sudo update-ca-certificates")
	}

	trustCmd.Stdout = os.Stdout
	trustCmd.Stderr = os.Stderr
	trustCmd.Stdin = os.Stdin

	if err := trustCmd.Run(); err != nil {
		fmt.Printf("\nManual install: sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s\n", certPath)
		return fmt.Errorf("trust install failed: %w", err)
	}

	fmt.Println("" + tick() + " CA certificate trusted. HTTPS will work for all *.test domains.")
	return nil
}

// loadProxyConfig builds a config.GlobalConfig with detected proxy ports.
// When withSecret is true, also resolves and attaches the installation secret
// key (needed for generating .env.container).
func loadProxyConfig(withSecret bool) (config.GlobalConfig, error) {
	httpPort, httpsPort, tls := DetectProxyPorts()
	cfg := config.WithPorts(httpPort, httpsPort, tls)
	if withSecret {
		secretKey, err := config.EnsureSecretKey()
		if err != nil {
			return cfg, fmt.Errorf("ensuring secret key: %w", err)
		}
		cfg.SecretKey = secretKey
	}
	return cfg, nil
}

func DetectProxyPorts() (httpPort, httpsPort int, tls bool) {
	if !isProxyRunning() {
		cfg, _ := config.LoadGlobal()
		return cfg.HTTPPort, cfg.HTTPSPort, cfg.TLS
	}
	out, err := exec.Command("docker", "port", proxyContainerName).Output()
	if err != nil {
		cfg, _ := config.LoadGlobal()
		return cfg.HTTPPort, cfg.HTTPSPort, cfg.TLS
	}

	httpPort = 80
	httpsPort = 443
	tls = false

	for _, line := range strings.Split(string(out), "\n") {
		// Format: "443/tcp -> 0.0.0.0:8443" or "443/tcp -> [::]:8443"
		// Skip IPv6 lines, parse IPv4 only
		if strings.Contains(line, "[::]:") {
			continue
		}
		parts := strings.SplitN(line, " -> ", 2)
		if len(parts) != 2 {
			continue
		}
		// parts[1] is "0.0.0.0:8443" or "127.0.0.1:2019"
		lastColon := strings.LastIndex(parts[1], ":")
		if lastColon < 0 {
			continue
		}
		port, err := strconv.Atoi(parts[1][lastColon+1:])
		if err != nil || port == 0 {
			continue
		}
		if strings.HasPrefix(parts[0], "443/") {
			httpsPort = port
			tls = true
		} else if strings.HasPrefix(parts[0], "80/") {
			httpPort = port
		}
	}
	return
}

func isProxyRunning() bool {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", proxyContainerName).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func isProxyAPIReady() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:2019/config/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}
