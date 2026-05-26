package config

import "testing"

func TestWorkspaceURL(t *testing.T) {
	tests := []struct {
		name     string
		cfg      GlobalConfig
		hostname string
		want     string
	}{
		{"standard https", GlobalConfig{TLS: true, HTTPSPort: 443}, "proj--feat", "https://proj--feat.test"},
		{"custom https port", GlobalConfig{TLS: true, HTTPSPort: 8443}, "proj--feat", "https://proj--feat.test:8443"},
		{"standard http", GlobalConfig{TLS: false, HTTPPort: 80}, "proj--feat", "http://proj--feat.test"},
		{"custom http port", GlobalConfig{TLS: false, HTTPPort: 8080}, "proj--feat", "http://proj--feat.test:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.WorkspaceURL(tt.hostname)
			if got != tt.want {
				t.Errorf("WorkspaceURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceURL(t *testing.T) {
	tests := []struct {
		name     string
		cfg      GlobalConfig
		hostname string
		want     string
	}{
		{"standard https", GlobalConfig{TLS: true, HTTPSPort: 443}, "proj--feat", "https://vite.proj--feat.test"},
		{"custom https port", GlobalConfig{TLS: true, HTTPSPort: 8443}, "proj--feat", "https://vite.proj--feat.test:8443"},
		{"http only", GlobalConfig{TLS: false, HTTPPort: 80}, "proj--feat", "http://vite.proj--feat.test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.ServiceURL("vite", tt.hostname)
			if got != tt.want {
				t.Errorf("ViteURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWithPorts(t *testing.T) {
	cfg := WithPorts(8080, 8443, true)
	if cfg.HTTPPort != 8080 {
		t.Errorf("HTTPPort = %d, want 8080", cfg.HTTPPort)
	}
	if cfg.HTTPSPort != 8443 {
		t.Errorf("HTTPSPort = %d, want 8443", cfg.HTTPSPort)
	}
	if !cfg.TLS {
		t.Error("TLS should be true")
	}
}
