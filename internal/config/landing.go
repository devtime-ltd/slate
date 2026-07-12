package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Custom landing commands run on the HOST from a repo-tracked slate.yml, so
// they need explicit one-time consent per project + command string, recorded
// outside the repo. Presets are slate-shipped and exempt.

func landingApprovalKey(mainRoot, command string) string {
	sum := sha256.Sum256([]byte(mainRoot + "\x00" + command))
	return hex.EncodeToString(sum[:])
}

func landingApprovalsPath() string {
	return filepath.Join(GlobalConfigDir(), "landing-approvals")
}

func LandingApproved(mainRoot, command string) bool {
	data, err := os.ReadFile(landingApprovalsPath())
	if err != nil {
		return false
	}
	key := landingApprovalKey(mainRoot, command)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == key {
			return true
		}
	}
	return false
}

func ApproveLanding(mainRoot, command string) error {
	if err := os.MkdirAll(GlobalConfigDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(landingApprovalsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, landingApprovalKey(mainRoot, command))
	return err
}
