package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandCommand(t *testing.T) {
	got := expandCommand(`claude --name "{{HOSTNAME}}" # {{PROJECT}}/{{WORKSPACE}}`, "api", "shop")
	want := `claude --name "shop--api" # shop/api`
	if got != want {
		t.Errorf("expandCommand = %q, want %q", got, want)
	}
}

func TestAgentFresh(t *testing.T) {
	cases := []struct {
		name     string
		marker   bool
		pending  bool
		bare     bool
		freshEnv string
		want     bool
	}{
		{"first entry via up hook", false, false, false, "1", true},
		{"first entry in bare workspace", false, false, true, "", true},
		{"first entry after non-interactive provision", false, true, false, "", true},
		{"existing pre-marker workspace", false, false, false, "", false},
		{"marker beats SLATE_FRESH", true, false, false, "1", false},
		{"marker beats bare", true, false, true, "", false},
		{"marker beats pending", true, true, false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wsDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(wsDir, ".slate"), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.marker {
				if err := os.WriteFile(agentStartedMarker(wsDir), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.pending {
				if err := os.WriteFile(agentPendingMarker(wsDir), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.bare {
				if err := os.WriteFile(unprovisionedMarker(wsDir), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("SLATE_FRESH", tc.freshEnv)
			if got := agentFresh(wsDir); got != tc.want {
				t.Fatalf("agentFresh = %v, want %v", got, tc.want)
			}
		})
	}
}
