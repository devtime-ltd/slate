package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ServicePorts maps subdomain prefixes to host ports.
// Empty key "" is the main app. "vite" becomes vite.hostname.test, etc.
type ServicePorts map[string]string

// caddyClient bounds every admin-API call so a stalled Caddy can't hang
// slate's lifecycle commands.
var caddyClient = &http.Client{Timeout: 5 * time.Second}

func caddyAvailable() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:2019/config/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// routeID names a route with its owning workspace as a distinct segment.
// Hostname suffix matching can't tell `foo.api--dev.test` (apex of a project
// named "foo.api") from subdomain foo of workspace api--dev; the ID segments
// can, so ownership-scoped cleanup keys on these.
func routeID(hostname, subdomain string) string {
	return "slate|" + hostname + "|" + subdomain
}

func Register(hostname string, services ServicePorts) error {
	if !caddyAvailable() {
		return fmt.Errorf("HTTPS proxy not running. Run `slate proxy start` or `slate setup`")
	}

	// Drop everything the workspace previously registered, so subdomains
	// removed or renamed since the last up don't linger.
	deleteWorkspaceRoutes(hostname)

	for subdomain, port := range services {
		if port == "" {
			continue
		}
		host := hostname
		if subdomain != "" {
			host = subdomain + "." + hostname
		}

		// Exact-host cleanup for routes predating the owned-ID scheme.
		deleteRoutesByExactHost(host + ".test")
		if err := caddyAddRoute(routeID(hostname, subdomain), host+".test", port); err != nil {
			return err
		}
	}
	return nil
}

// UnregisterAll removes the workspace's apex route and every subdomain route,
// regardless of which config registered them.
func UnregisterAll(hostname string) error {
	if !caddyAvailable() {
		return nil
	}
	deleteWorkspaceRoutes(hostname)
	return nil
}

// deleteWorkspaceRoutes drops the workspace's routes under both ID schemes:
// the current owned IDs, and pre-upgrade `slate__<host>` IDs matched by host
// (apex or subdomain). The legacy match reintroduces the dotted-project
// ambiguity, but only for routes that disappear entirely after one
// re-registration under the owned scheme.
func deleteWorkspaceRoutes(hostname string) {
	ownedPrefix := "slate|" + hostname + "|"
	apex := hostname + ".test"
	suffix := "." + apex
	deleteRoutesWhere(func(id, host string) bool {
		if strings.HasPrefix(id, ownedPrefix) {
			return true
		}
		return strings.HasPrefix(id, "slate__") && (host == apex || strings.HasSuffix(host, suffix))
	})
}

func deleteRoutesByExactHost(host string) {
	deleteRoutesWhere(func(_, h string) bool {
		return h == host
	})
}

// deleteRoutesWhere removes any registered route whose (id, match.host)
// satisfies the predicate. Host matching also cleans up routes with stale IDs
// (e.g. left over from a slate upgrade that changed the ID scheme).
func deleteRoutesWhere(match func(id, host string) bool) {
	resp, err := caddyClient.Get("http://127.0.0.1:2019/config/apps/http/servers/slate/routes")
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
	err = json.NewDecoder(resp.Body).Decode(&routes)
	io.Copy(io.Discard, resp.Body)
	if err != nil {
		return
	}

	for _, r := range routes {
		for _, m := range r.Match {
			for _, h := range m.Host {
				if match(r.ID, h) {
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

	resp, err := caddyClient.Post("http://127.0.0.1:2019/config/apps/http/servers/slate/routes",
		"application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("caddy API: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("caddy API returned %d", resp.StatusCode)
	}
	return nil
}

func caddyDeleteRoute(id string) {
	// The owned-ID scheme's "|" separator is not a valid raw path character.
	req, err := http.NewRequest("DELETE", "http://127.0.0.1:2019/id/"+url.PathEscape(id), nil)
	if err != nil {
		return
	}
	resp, err := caddyClient.Do(req)
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}
