package config

import "testing"

func TestLandingApproval(t *testing.T) {
	t.Setenv("SLATE_CONFIG_DIR", t.TempDir())

	if LandingApproved("/proj", "tmux new -A") {
		t.Fatal("nothing approved yet")
	}
	if err := ApproveLanding("/proj", "tmux new -A"); err != nil {
		t.Fatal(err)
	}
	if !LandingApproved("/proj", "tmux new -A") {
		t.Error("approved command should be remembered")
	}
	if LandingApproved("/proj", "tmux new -A -s other") {
		t.Error("different command must not inherit approval")
	}
	if LandingApproved("/other-proj", "tmux new -A") {
		t.Error("different project must not inherit approval")
	}
}
