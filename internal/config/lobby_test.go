package config

import "testing"

func TestLobbyApproval(t *testing.T) {
	t.Setenv("SLATE_CONFIG_DIR", t.TempDir())

	if LobbyApproved("/proj", "tmux new -A") {
		t.Fatal("nothing approved yet")
	}
	if err := ApproveLobby("/proj", "tmux new -A"); err != nil {
		t.Fatal(err)
	}
	if !LobbyApproved("/proj", "tmux new -A") {
		t.Error("approved command should be remembered")
	}
	if LobbyApproved("/proj", "tmux new -A -s other") {
		t.Error("different command must not inherit approval")
	}
	if LobbyApproved("/other-proj", "tmux new -A") {
		t.Error("different project must not inherit approval")
	}
}
