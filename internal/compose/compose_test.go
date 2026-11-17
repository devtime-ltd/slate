package compose

import (
	"slices"
	"testing"
)

func TestAppLikeFromConfigJSON(t *testing.T) {
	data := []byte(`{"services": {
		"abc":     {"volumes": [{"type": "bind", "source": "/x", "target": "/app"}]},
		"app":     {"volumes": [{"type": "bind", "source": "/x", "target": "/app"}]},
		"zed":     {"volumes": [{"type": "bind", "source": "/x", "target": "/app"}]},
		"mysql":   {"volumes": [{"type": "volume", "target": "/var/lib/mysql"}]},
		"mailpit": {}
	}}`)

	got, err := appLikeFromConfigJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"app", "abc", "zed"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v (app first, rest sorted)", got, want)
	}
}

func TestAppLikeFromConfigJSONNoAppBinds(t *testing.T) {
	got, err := appLikeFromConfigJSON([]byte(`{"services": {"db": {"volumes": [{"type": "volume", "target": "/data"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestAppLikeFromConfigJSONInvalid(t *testing.T) {
	if _, err := appLikeFromConfigJSON([]byte("not json")); err == nil {
		t.Error("invalid JSON should error")
	}
}

func TestWorktreeBindsFromConfigJSON(t *testing.T) {
	data := []byte(`{"services": {
		"app":     {"volumes": [{"type": "bind", "source": "/ws", "target": "/app"}]},
		"vite":    {"volumes": [{"type": "bind", "source": "/ws/", "target": "/srv"}]},
		"other":   {"volumes": [{"type": "bind", "source": "/elsewhere", "target": "/app"}]},
		"mysql":   {"volumes": [{"type": "volume", "source": "data", "target": "/var/lib/mysql"}]},
		"mailpit": {}
	}}`)

	got, err := worktreeBindsFromConfigJSON(data, "/ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["app"] != "/app" || got["vite"] != "/srv" {
		t.Errorf("got %v, want app=/app and vite=/srv only", got)
	}
}
