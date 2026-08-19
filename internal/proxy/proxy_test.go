package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubCaddy records every mutating call so a test can assert not just the
// outcome but which admin endpoint got used - the whole point of these fixes
// is that `/load` stops being reachable on a config slate does not own.
type stubCaddy struct {
	listen    []string // nil means no `slate` server exists
	hasTLS    bool
	calls     []string // "METHOD path"
	rejectPUT bool     // model Caddy refusing a duplicate listener address
}

func (s *stubCaddy) start(t *testing.T) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			body, _ := io.ReadAll(r.Body)
			s.calls = append(s.calls, r.Method+" "+r.URL.Path)

			if s.rejectPUT && r.Method == http.MethodPut {
				http.Error(w, "duplicate listener address :443", http.StatusBadRequest)
				return
			}

			switch r.URL.Path {
			case "/config/apps/http/servers/slate/listen":
				_ = json.Unmarshal(body, &s.listen)
			case "/config/apps/http/servers/slate":
				var server struct {
					Listen []string `json:"listen"`
				}
				_ = json.Unmarshal(body, &server)
				s.listen = server.Listen
			case "/config/apps/tls":
				s.hasTLS = true
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		switch r.URL.Path {
		case "/config/apps/http/servers/slate", "/config/apps/http/servers/slate/listen":
			if s.listen == nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(s.listen)
		case "/config/apps/tls":
			if !s.hasTLS {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	prevBase := adminBase
	adminBase = srv.URL
	t.Cleanup(func() { adminBase = prevBase })
}

func (s *stubCaddy) called(method, path string) bool {
	for _, c := range s.calls {
		if c == method+" "+path {
			return true
		}
	}
	return false
}

func withOwnership(t *testing.T, owns bool) {
	t.Helper()
	prev := slateOwnsProxy
	slateOwnsProxy = func() bool { return owns }
	t.Cleanup(func() { slateOwnsProxy = prev })
}

// The P1: slate treats any admin API on the port as usable, including a Caddy
// the user runs for their own sites. Such a Caddy has no `slate` server, and
// POSTing /load would replace its entire configuration.
func TestEnsureServerNeverLoadsOverAForeignCaddy(t *testing.T) {
	stub := &stubCaddy{listen: nil, hasTLS: true}
	stub.start(t)
	withOwnership(t, false)

	if err := EnsureServer(true); err != nil {
		t.Fatalf("EnsureServer() error = %v", err)
	}

	if stub.called("POST", "/load") {
		t.Error("EnsureServer() used /load on a Caddy slate does not own, replacing the whole config")
	}
	if !stub.called("PUT", "/config/apps/http/servers/slate") {
		t.Errorf("EnsureServer() should add the server node additively; calls = %v", stub.calls)
	}
}

func TestEnsureServerLeavesAnExistingTLSAppAlone(t *testing.T) {
	stub := &stubCaddy{listen: nil, hasTLS: true}
	stub.start(t)
	withOwnership(t, false)

	if err := EnsureServer(true); err != nil {
		t.Fatalf("EnsureServer() error = %v", err)
	}
	if stub.called("PUT", "/config/apps/tls") {
		t.Error("EnsureServer() overwrote the host Caddy's existing TLS policies")
	}
}

func TestEnsureServerAddsTLSAutomationWhenAbsent(t *testing.T) {
	stub := &stubCaddy{listen: nil, hasTLS: false}
	stub.start(t)
	withOwnership(t, false)

	if err := EnsureServer(true); err != nil {
		t.Fatalf("EnsureServer() error = %v", err)
	}
	if !stub.called("PUT", "/config/apps/tls") {
		t.Errorf("EnsureServer() should enable internal certs when the host has no TLS app; calls = %v", stub.calls)
	}
}

func TestEnsureServerRebuildsSlateOwnedProxyFromEmpty(t *testing.T) {
	// slate's own container boots from a bare Caddyfile with no `slate` server.
	// There are no routes to lose, so the wholesale reload stays correct here.
	stub := &stubCaddy{listen: nil}
	stub.start(t)
	withOwnership(t, true)

	if err := EnsureServer(true); err != nil {
		t.Fatalf("EnsureServer() error = %v", err)
	}
	if !stub.called("POST", "/load") {
		t.Errorf("EnsureServer() should reload slate's own empty proxy; calls = %v", stub.calls)
	}
}

// The second P1: a TLS-mode change made the listeners mismatch, and the old
// code answered by reloading a base config with routes: [], dropping every
// registered route across all workspaces.
func TestEnsureServerPatchesListenersWithoutDroppingRoutes(t *testing.T) {
	stub := &stubCaddy{listen: []string{":80"}}
	stub.start(t)
	withOwnership(t, true)

	if err := EnsureServer(true); err != nil {
		t.Fatalf("EnsureServer() error = %v", err)
	}

	if stub.called("POST", "/load") {
		t.Error("EnsureServer() reloaded the whole config to change listeners, dropping every route")
	}
	if !stub.called("PATCH", "/config/apps/http/servers/slate/listen") {
		t.Errorf("EnsureServer() should patch only the listen field; calls = %v", stub.calls)
	}
	if got, want := strings.Join(stub.listen, ","), ":80,:443"; got != want {
		t.Errorf("listeners = %q, want %q", got, want)
	}
}

func TestEnsureServerIsANoOpWhenListenersAlreadyMatch(t *testing.T) {
	stub := &stubCaddy{listen: []string{":80", ":443"}}
	stub.start(t)
	withOwnership(t, true)

	if err := EnsureServer(true); err != nil {
		t.Fatalf("EnsureServer() error = %v", err)
	}
	if len(stub.calls) != 0 {
		t.Errorf("EnsureServer() mutated a matching config; calls = %v", stub.calls)
	}
}

// Caddy may return listeners in any order; treating that as a mismatch would
// trigger a needless reconfigure.
func TestEnsureServerToleratesListenerOrdering(t *testing.T) {
	stub := &stubCaddy{listen: []string{":443", ":80"}}
	stub.start(t)
	withOwnership(t, true)

	if err := EnsureServer(true); err != nil {
		t.Fatalf("EnsureServer() error = %v", err)
	}
	if len(stub.calls) != 0 {
		t.Errorf("EnsureServer() reconfigured over listener ordering alone; calls = %v", stub.calls)
	}
}

func TestSameListeners(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"identical", []string{":80", ":443"}, []string{":80", ":443"}, true},
		{"reordered", []string{":443", ":80"}, []string{":80", ":443"}, true},
		{"different length", []string{":80"}, []string{":80", ":443"}, false},
		{"different member", []string{":80", ":8443"}, []string{":80", ":443"}, false},
		{"duplicates are not a set collapse", []string{":80", ":80"}, []string{":80", ":443"}, false},
		{"both empty", nil, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameListeners(tc.a, tc.b); got != tc.want {
				t.Errorf("sameListeners(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// Caddy requires listener addresses to be unique across servers, so adding a
// `slate` server beside a host Caddy already serving :80/:443 is rejected.
// slate must surface that and stop, never fall back to replacing the config.
func TestEnsureServerRefusesRatherThanLoadingWhenListenersConflict(t *testing.T) {
	stub := &stubCaddy{listen: nil, hasTLS: true, rejectPUT: true}
	stub.start(t)
	withOwnership(t, false)

	err := EnsureServer(true)
	if err == nil {
		t.Fatal("EnsureServer() should fail when the host Caddy owns the listeners")
	}
	if stub.called("POST", "/load") {
		t.Error("EnsureServer() fell back to /load, replacing a config slate does not own")
	}
	if !strings.Contains(err.Error(), "slate proxy start") {
		t.Errorf("error should tell the user how to proceed; got %q", err)
	}
}

func TestAdminWriteNamesTheEndpoint(t *testing.T) {
	stub := &stubCaddy{listen: nil, hasTLS: true, rejectPUT: true}
	stub.start(t)

	err := adminWrite("PUT", "/config/apps/http/servers/slate", strings.NewReader("{}"))
	if err == nil {
		t.Fatal("adminWrite() should surface a 4xx")
	}
	for _, want := range []string{"PUT", "/config/apps/http/servers/slate", "400"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}
