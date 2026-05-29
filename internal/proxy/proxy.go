package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ServicePorts maps subdomain prefixes to host ports.
// Empty key "" is the main app. "vite" becomes vite.hostname.test, etc.
type ServicePorts map[string]string

func caddyAvailable() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:2019/config/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func Register(hostname string, services ServicePorts) error {
	if !caddyAvailable() {
		return fmt.Errorf("HTTPS proxy not running. Run `slate proxy start` or `slate setup`")
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

		fullHost := host + ".test"
		deleteRoutesByHost(fullHost)
		if err := caddyAddRoute(routeID, fullHost, port); err != nil {
			return err
		}
	}
	return nil
}

func Unregister(hostname string, subdomains []string) error {
	if !caddyAvailable() {
		return nil
	}

	hosts := []string{hostname + ".test"}
	for _, sub := range subdomains {
		hosts = append(hosts, sub+"."+hostname+".test")
	}

	for _, host := range hosts {
		deleteRoutesByHost(host)
	}
	return nil
}

// deleteRoutesByHost removes any registered route whose match.host contains
// the given host. Handles cleanup of routes with stale IDs (e.g. left over
// from a slate upgrade that changed the ID scheme).
func deleteRoutesByHost(host string) {
	resp, err := http.Get("http://127.0.0.1:2019/config/apps/http/servers/slate/routes")
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var routes []struct {
		ID    string `json:"@id"`
		Match []struct {
			Host []string `json:"host"`
		} `json:"match"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&routes); err != nil {
		return
	}

	for _, r := range routes {
		for _, m := range r.Match {
			for _, h := range m.Host {
				if h == host {
					caddyDeleteRoute(r.ID)
				}
			}
		}
	}
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
