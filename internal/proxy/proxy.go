package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/devtime-ltd/slate/internal/config"
)

type backend string

const (
	backendCaddy backend = "caddy"
	backendHerd  backend = "herd"
)

// ServicePorts maps subdomain prefixes to host ports.
// Empty key "" is the main app. "vite" becomes vite.hostname.test, etc.
type ServicePorts map[string]string

func detect(cfg config.GlobalConfig) (backend, error) {
	pref := strings.ToLower(cfg.Proxy)

	if pref == "caddy" || pref == "auto" || pref == "" {
		if caddyAvailable() {
			return backendCaddy, nil
		}
	}
	if pref == "herd" || pref == "auto" || pref == "" {
		if herdAvailable() {
			return backendHerd, nil
		}
	}
	if pref != "auto" && pref != "" {
		return "", fmt.Errorf("configured proxy '%s' is not available", pref)
	}
	return "", fmt.Errorf("no HTTPS proxy running. Run `slate proxy start` or install Caddy/Herd")
}

func caddyAvailable() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:2019/config/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func herdAvailable() bool {
	_, err := exec.LookPath("herd")
	return err == nil
}

func Register(cfg config.GlobalConfig, hostname string, services ServicePorts) error {
	b, err := detect(cfg)
	if err != nil {
		return err
	}

	for subdomain, port := range services {
		if port == "" {
			continue
		}
		host := hostname
		routeID := "slate__" + hostname
		if subdomain != "" {
			host = subdomain + "." + hostname
			routeID = "slate__" + subdomain + "." + hostname
		}

		switch b {
		case backendHerd:
			exec.Command("herd", "proxy", host, "http://127.0.0.1:"+port, "--secure").Run()
		case backendCaddy:
			caddyDeleteRoute(routeID)
			caddyAddRoute(routeID, host+".test", port)
		}
	}
	return nil
}

func Unregister(cfg config.GlobalConfig, hostname string, subdomains []string) error {
	b, err := detect(cfg)
	if err != nil {
		return nil
	}

	hosts := []string{hostname}
	for _, sub := range subdomains {
		hosts = append(hosts, sub+"."+hostname)
	}

	for _, host := range hosts {
		routeID := "slate__" + host
		switch b {
		case backendHerd:
			exec.Command("herd", "unproxy", host).Run()
		case backendCaddy:
			caddyDeleteRoute(routeID)
		}
	}
	return nil
}

func caddyAddRoute(id, host, port string) error {
	upstream := "host.docker.internal:" + port

	payload := map[string]interface{}{
		"@id":      id,
		"match":    []map[string]interface{}{{"host": []string{host}}},
		"handle":   []map[string]interface{}{{"handler": "reverse_proxy", "upstreams": []map[string]string{{"dial": upstream}}}},
		"terminal": true,
	}
	body, _ := json.Marshal(payload)

	caddyDeleteRoute(id)

	resp, err := http.Post("http://127.0.0.1:2019/config/apps/http/servers/slate/routes",
		"application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("caddy API: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("caddy API returned %d", resp.StatusCode)
	}
	return nil
}

func caddyDeleteRoute(id string) {
	req, _ := http.NewRequest("DELETE", "http://127.0.0.1:2019/id/"+id, nil)
	http.DefaultClient.Do(req)
}
