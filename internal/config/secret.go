package config

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

func GenerateSecretKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating secret key: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func DerivePassword(secretKey, project, workspace, salt string) string {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(project + ":" + workspace + ":" + salt))
	raw := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(raw)[:24]
}

func EnsureSecretKey() (string, error) {
	cfg, err := LoadGlobal()
	if err != nil {
		return "", err
	}
	if cfg.SecretKey != "" {
		return cfg.SecretKey, nil
	}
	key, err := GenerateSecretKey()
	if err != nil {
		return "", err
	}
	cfg.SecretKey = key
	if err := SaveGlobal(cfg); err != nil {
		return "", err
	}
	return cfg.SecretKey, nil
}

var genPasswordRe = regexp.MustCompile(`\{\{GEN_PASSWORD:([^}]+)\}\}`)

func ExpandPasswords(value, secretKey, project, workspace string) string {
	return genPasswordRe.ReplaceAllStringFunc(value, func(match string) string {
		sub := genPasswordRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		salt := strings.TrimSpace(sub[1])
		return DerivePassword(secretKey, project, workspace, salt)
	})
}
