package cmd

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestExpandCommand(t *testing.T) {
	got := expandCommand(`claude --name "{{HOSTNAME}}" # {{PROJECT}}/{{WORKSPACE}}`, "api", "shop")
	want := `claude --name "shop--api" # shop/api`
	if got != want {
		t.Errorf("expandCommand = %q, want %q", got, want)
	}
}

func TestAppendShellArgs(t *testing.T) {
	cases := []struct {
		name    string
		command string
		args    []string
		want    string
	}{
		{"no args", "claude", nil, "claude"},
		{"plain", "claude", []string{"--continue"}, "claude '--continue'"},
		{"spaces and quotes", "claude", []string{`triage today's logs`}, `claude 'triage today'\''s logs'`},
		{"placeholders are literal", "claude", []string{"{{PROJECT}}"}, "claude '{{PROJECT}}'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendShellArgs(tc.command, tc.args); got != tc.want {
				t.Fatalf("appendShellArgs = %q, want %q", got, tc.want)
			}
		})
	}
}

// The appended args are shell text, so the guarantee that matters is what the
// process actually receives.
func TestAppendShellArgsSurvivesTheShell(t *testing.T) {
	args := []string{"--rc", "sparta--log-triage", `it's "quoted" $HOME; rm -rf /`}
	out, err := exec.Command("sh", "-c", appendShellArgs("printf '%s\\n'", args)).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if !slices.Equal(got, args) {
		t.Fatalf("shell delivered %q, want %q", got, args)
	}
}

func TestSlateBinEnvPrefersTheRunningBinary(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	env := slateBinEnv()
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path on this platform")
	}
	if want := "SLATE_BIN=" + exe; !slices.Contains(env, want) {
		t.Errorf("slateBinEnv = %q, want it to contain %q", env, want)
	}
	want := "PATH=" + filepath.Dir(exe) + string(os.PathListSeparator) + "/usr/bin:/bin"
	if !slices.Contains(env, want) {
		t.Errorf("slateBinEnv = %q, want it to contain %q", env, want)
	}
}

func TestSplitAtDash(t *testing.T) {
	cases := []struct {
		name       string
		argv       []string
		wantTarget []string
		wantExtra  []string
	}{
		{"nothing", nil, nil, nil},
		{"workspace only", []string{"api"}, []string{"api"}, nil},
		{"passthrough only", []string{"--", "--continue"}, []string{}, []string{"--continue"}},
		{"both", []string{"api", "--", "-p", "go"}, []string{"api"}, []string{"-p", "go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotTarget, gotExtra []string
			c := &cobra.Command{
				Use:  "agent",
				Args: cobra.ArbitraryArgs,
				RunE: func(cmd *cobra.Command, args []string) error {
					gotTarget, gotExtra = splitAtDash(cmd, args)
					return nil
				},
			}
			c.SetArgs(tc.argv)
			c.SetOut(io.Discard)
			if err := c.Execute(); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(gotTarget, tc.wantTarget) {
				t.Errorf("target = %q, want %q", gotTarget, tc.wantTarget)
			}
			if !slices.Equal(gotExtra, tc.wantExtra) {
				t.Errorf("extra = %q, want %q", gotExtra, tc.wantExtra)
			}
		})
	}
}

func TestResolveHooks(t *testing.T) {
	cases := []struct {
		name  string
		optIn string
		flag  string
		want  bool
	}{
		// No flag and no opt-in leaves the same terminal gate as auto_cd.
		{"unset", "", "", isInteractiveTerminal()},
		{"opt-in via env", "1", "", true},
		{"flag beats env", "1", "false", false},
		{"flag alone", "", "true", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SLATE_HOOKS", tc.optIn)
			var flag, got bool
			c := &cobra.Command{Use: "new", RunE: func(cmd *cobra.Command, args []string) error {
				got = resolveHooks(cmd, "hooks", flag)
				return nil
			}}
			c.Flags().BoolVar(&flag, "hooks", false, "")
			var argv []string
			if tc.flag != "" {
				argv = append(argv, "--hooks="+tc.flag)
			}
			c.SetArgs(argv)
			c.SetOut(io.Discard)
			if err := c.Execute(); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("resolveHooks = %v, want %v", got, tc.want)
			}
		})
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
