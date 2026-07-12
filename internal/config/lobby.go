package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Custom lobby commands run on the HOST from a repo-tracked slate.yml, so
// they need explicit one-time consent per project + command string, recorded
// outside the repo. Presets are slate-shipped and exempt.

func lobbyApprovalKey(mainRoot, command string) string {
	sum := sha256.Sum256([]byte(mainRoot + "\x00" + command))
	return hex.EncodeToString(sum[:])
}

func lobbyApprovalsPath() string {
	return filepath.Join(GlobalConfigDir(), "lobby-approvals")
}

func LobbyApproved(mainRoot, command string) bool {
	data, err := os.ReadFile(lobbyApprovalsPath())
	if err != nil {
		return false
	}
	key := lobbyApprovalKey(mainRoot, command)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == key {
			return true
		}
	}
	return false
}

func ApproveLobby(mainRoot, command string) error {
	if err := os.MkdirAll(GlobalConfigDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(lobbyApprovalsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, lobbyApprovalKey(mainRoot, command))
	return err
}
