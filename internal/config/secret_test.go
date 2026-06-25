package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateSecretKey(t *testing.T) {
	key1, err := GenerateSecretKey()
	if err != nil {
		t.Fatal(err)
	}
	key2, err := GenerateSecretKey()
	if err != nil {
		t.Fatal(err)
	}
	if key1 == key2 {
		t.Error("two generated keys should not be equal")
	}
	if len(key1) != 64 {
		t.Errorf("key length = %d, want 64 hex chars", len(key1))
	}
}

func TestDerivePassword(t *testing.T) {
	key := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

	p1 := DerivePassword(key, "proj", "ws1", "mysql")
	p2 := DerivePassword(key, "proj", "ws1", "mysql")
	if p1 != p2 {
		t.Error("same inputs should produce same password")
	}

	p3 := DerivePassword(key, "proj", "ws1", "redis")
	if p1 == p3 {
		t.Error("different salt should produce different password")
	}

	p4 := DerivePassword(key, "proj", "ws2", "mysql")
	if p1 == p4 {
		t.Error("different workspace should produce different password")
	}

	p5 := DerivePassword(key, "other", "ws1", "mysql")
	if p1 == p5 {
		t.Error("different project should produce different password")
	}

	if len(p1) != 24 {
		t.Errorf("password length = %d, want 24", len(p1))
	}
}

func TestExpandPasswords(t *testing.T) {
	key := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

	input := "password={{GEN_PASSWORD:mysql}}"
	result := ExpandPasswords(input, key, "proj", "ws")

	if result == input {
		t.Error("placeholder should be expanded")
	}
	expected := "password=" + DerivePassword(key, "proj", "ws", "mysql")
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestExpandPasswordsMultiple(t *testing.T) {
	key := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

	input := "db={{GEN_PASSWORD:mysql}} cache={{GEN_PASSWORD:redis}}"
	result := ExpandPasswords(input, key, "proj", "ws")

	dbPw := DerivePassword(key, "proj", "ws", "mysql")
	redisPw := DerivePassword(key, "proj", "ws", "redis")

	if result != "db="+dbPw+" cache="+redisPw {
		t.Errorf("multiple placeholders not expanded correctly: %q", result)
	}
}

func TestExpandPasswordsNoPlaceholder(t *testing.T) {
	result := ExpandPasswords("plain-value", "key", "proj", "ws")
	if result != "plain-value" {
		t.Errorf("should return input unchanged, got %q", result)
	}
}

func TestDeriveAppKey(t *testing.T) {
	key := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	got := DeriveAppKey(key, "proj", "ws")

	if !strings.HasPrefix(got, "base64:") {
		t.Fatalf("APP_KEY missing base64: prefix: %q", got)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "base64:"))
	if err != nil {
		t.Fatalf("APP_KEY not valid base64: %v", err)
	}
	if len(raw) != 32 {
		t.Errorf("APP_KEY decodes to %d bytes, Laravel AES-256 needs 32", len(raw))
	}
	if other := DeriveAppKey(key, "proj", "other"); got == other {
		t.Error("different workspaces should derive different keys")
	}
	if again := DeriveAppKey(key, "proj", "ws"); got != again {
		t.Error("derivation must be stable for the same inputs")
	}
}

func TestExpandAppKey(t *testing.T) {
	key := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	result := ExpandAppKey("APP_KEY={{GEN_APP_KEY}}", key, "proj", "ws")
	expected := "APP_KEY=" + DeriveAppKey(key, "proj", "ws")
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
	if unchanged := ExpandAppKey("plain", key, "proj", "ws"); unchanged != "plain" {
		t.Errorf("no placeholder should pass through, got %q", unchanged)
	}
}
