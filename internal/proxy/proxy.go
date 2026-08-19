package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// ServicePorts maps subdomain prefixes to host ports.
// Empty key "" is the main app. "vite" becomes vite.hostname.test, etc.
type ServicePorts map[string]string

// caddyClient bounds every admin-API call so a stalled Caddy can't hang
// slate's lifecycle commands.
var caddyClient = &http.Client{Timeout: 5 * time.Second}

// adminBase is the Caddy admin API root. A variable so tests can point the
// package at a stub server.
var adminBase = "http://127.0.0.1:2019"

// slateOwnsProxy reports whether the Caddy on the admin port is the container
// slate runs, rather than one the user runs for their own sites. Only a proxy
// slate owns may have its configuration replaced wholesale. A variable so tests
// can drive both cases without docker.
var slateOwnsProxy = func() bool {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", ProxyContainerName).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// ProxyContainerName is the container slate runs Caddy in.
const ProxyContainerName = "slate-proxy"

func adminURL(path string) string { return adminBase + path }

func caddyAvailable() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(adminURL("/config/"))
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

	// A proxy container that restarted outside slate's control (host reboot,
	// docker restart) boots from the bare Caddyfile and loses the API-loaded
	// server config, so every registration would 500. Rebuild it first.
	if !serverExists() {
		if err := EnsureServer(detectTLS()); err != nil {
			return fmt.Errorf("proxy server config missing and could not be rebuilt: %w", err)
		}
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
	resp, err := caddyClient.Get(adminURL("/config/apps/http/servers/slate/routes"))
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

func serverExists() bool {
	resp, err := caddyClient.Get(adminURL("/config/apps/http/servers/slate"))
	if err != nil {
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode < 400
}

// detectTLS infers the proxy's TLS mode from the running config. Only called
// when the server node is missing, i.e. on a Caddyfile-boot config, where the
// TLS-mode Caddyfile (`local_certs`) yields an apps.tls node and the no-TLS
// one (`auto_https off`) doesn't.
func detectTLS() bool {
	resp, err := caddyClient.Get(adminURL("/config/apps/tls"))
	if err != nil {
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode < 400
}

func expectedListen(tls bool) []string {
	if tls {
		return []string{":80", ":443"}
	}
	return []string{":80"}
}

// EnsureServer makes sure the running config contains the `slate` server with
// the expected listeners, without ever discarding configuration slate does not
// own. Registered routes survive in every path.
//
// The wholesale `/load` is reserved for a proxy slate owns whose `slate` server
// is absent, because /load replaces the *entire* Caddy configuration. slate treats any
// admin API on the port as usable, including a Caddy the user runs for their
// own sites, and such a Caddy has no server named `slate` - so an unguarded
// rebuild would take every unrelated site offline on the first `slate up`.
func EnsureServer(tls bool) error {
	want := expectedListen(tls)

	if listen, ok := serverListen(); ok {
		if sameListeners(listen, want) {
			return nil
		}
		// The server is there with the wrong listeners, which happens when the
		// TLS mode changes. Patch just that field: reloading the base config
		// here would silently drop every route already registered on it, so
		// each workspace would look healthy but be unreachable until its next
		// `slate up`.
		return patchListen(want)
	}

	// No `slate` server. On a foreign Caddy, add ours alongside whatever is
	// already running rather than replacing it.
	if !slateOwnsProxy() {
		return addServerNode(want)
	}

	listenJSON, _ := json.Marshal(want)
	cfg := fmt.Sprintf(`{
		"admin": {"listen": "0.0.0.0:2019"},
		"apps": {
			"http": {
				"servers": {
					"slate": {"listen": %s, "routes": []}
				}
			},
			"tls": {
				"automation": {
					"policies": [{"issuers": [{"module": "internal"}]}]
				}
			}
		}
	}`, listenJSON)

	return adminWrite("POST", "/load", strings.NewReader(cfg))
}

// patchListen replaces only the server's listener list.
func patchListen(listen []string) error {
	body, err := json.Marshal(listen)
	if err != nil {
		return err
	}
	return adminWrite("PATCH", "/config/apps/http/servers/slate/listen", bytes.NewReader(body))
}

// addServerNode adds the `slate` server to a Caddy slate does not own, leaving
// every other server in place. It fails rather than falling back to `/load`:
// on someone else's Caddy, refusing is always better than replacing.
func addServerNode(listen []string) error {
	server := map[string]any{"listen": listen, "routes": []any{}}
	body, err := json.Marshal(server)
	if err != nil {
		return err
	}

	if err := adminWrite("PUT", "/config/apps/http/servers/slate", bytes.NewReader(body)); err != nil {
		// Caddy requires listener addresses to be unique across servers, so this
		// is expected whenever the host Caddy already serves those ports. slate
		// cannot add itself alongside, and will not replace what is there.
		return fmt.Errorf("could not add a `slate` server to the Caddy already running on the admin port: %w\n\n"+
			"That Caddy most likely already has a server on %s, and Caddy does not allow two servers to "+
			"share a listener. slate will not replace a configuration it does not own, so either stop that "+
			"Caddy and run `slate proxy start`, or move its sites into a server named `slate` that slate can "+
			"add routes to",
			err, strings.Join(listen, ", "))
	}

	// Internal-issuer automation, added only when the host Caddy has no TLS
	// app at all, so an existing policy set is never overwritten.
	if !detectTLS() {
		automation := map[string]any{
			"automation": map[string]any{
				"policies": []any{map[string]any{"issuers": []any{map[string]any{"module": "internal"}}}},
			},
		}
		body, err := json.Marshal(automation)
		if err != nil {
			return err
		}
		if err := adminWrite("PUT", "/config/apps/tls", bytes.NewReader(body)); err != nil {
			return fmt.Errorf("added the `slate` server but could not enable internal certificates: %w", err)
		}
	}
	return nil
}

// adminWrite performs a mutating admin-API call and surfaces Caddy's own error
// body, which is the only thing that explains most failures.
func adminWrite(method, path string, body io.Reader) error {
	req, err := http.NewRequest(method, adminURL(path), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := caddyClient.Do(req)
	if err != nil {
		return fmt.Errorf("caddy API %s %s: %w", method, path, err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("caddy API %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func serverListen() (listen []string, ok bool) {
	resp, err := caddyClient.Get(adminURL("/config/apps/http/servers/slate/listen"))
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		io.Copy(io.Discard, resp.Body)
		return nil, false
	}
	if err := json.NewDecoder(resp.Body).Decode(&listen); err != nil {
		return nil, false
	}
	return listen, true
}

// sameListeners compares listener lists as sets. Caddy is free to return them
// in any order, and a spurious mismatch would trigger a needless reconfigure.
func sameListeners(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
		if seen[v] < 0 {
			return false
		}
	}
	return true
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

	resp, err := caddyClient.Post(adminURL("/config/apps/http/servers/slate/routes"),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("caddy API: %w", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("caddy API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func caddyDeleteRoute(id string) {
	// The owned-ID scheme's "|" separator is not a valid raw path character.
	req, err := http.NewRequest("DELETE", adminURL("/id/"+url.PathEscape(id)), nil)
	if err != nil {
		return
	}
	resp, err := caddyClient.Do(req)
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}
