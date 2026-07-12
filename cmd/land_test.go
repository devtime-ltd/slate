package cmd

import "testing"

func TestExpandLanding(t *testing.T) {
	got := expandLanding(`claude --resume "{{HOSTNAME}}" # {{PROJECT}}/{{WORKSPACE}}`, "api", "shop")
	want := `claude --resume "shop--api" # shop/api`
	if got != want {
		t.Errorf("expandLanding = %q, want %q", got, want)
	}
}
