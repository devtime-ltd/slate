package workspace

import "testing"

func TestValidateName(t *testing.T) {
	valid := []string{"a", "foo", "fix-bug", "feat123", "a1"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []struct {
		name string
		desc string
	}{
		{"", "empty"},
		{"A", "uppercase"},
		{"foo_bar", "underscore"},
		{"-foo", "starts with dash"},
		{"foo-", "ends with dash"},
		{"1foo", "starts with digit"},
		{"abcdefghijklmnopqrstuvwxyz1234567", "too long (33 chars)"},
		{"main", "reserved"},
		{"master", "reserved"},
		{"default", "reserved"},
		{"all", "reserved"},
	}
	for _, tt := range invalid {
		t.Run(tt.desc, func(t *testing.T) {
			if err := ValidateName(tt.name); err == nil {
				t.Errorf("ValidateName(%q) = nil, want error", tt.name)
			}
		})
	}
}

func TestValidateNameMaxLength(t *testing.T) {
	name32 := "abcdefghijklmnopqrstuvwxyz123456"
	if len(name32) != 32 {
		t.Fatalf("test name is %d chars, want 32", len(name32))
	}
	if err := ValidateName(name32); err != nil {
		t.Errorf("32-char name should be valid: %v", err)
	}

	name33 := name32 + "7"
	if err := ValidateName(name33); err == nil {
		t.Error("33-char name should be invalid")
	}
}
