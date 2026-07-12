package cmd

import "testing"

func TestExpandLobby(t *testing.T) {
	got := expandLobby(`claude --resume "{{HOSTNAME}}" # {{PROJECT}}/{{WORKSPACE}}`, "api", "shop")
	want := `claude --resume "shop--api" # shop/api`
	if got != want {
		t.Errorf("expandLobby = %q, want %q", got, want)
	}
}
