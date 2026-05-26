package config

import "fmt"

func (c GlobalConfig) WorkspaceURL(hostname string) string {
	if c.TLS {
		if c.HTTPSPort == 443 {
			return fmt.Sprintf("https://%s.test", hostname)
		}
		return fmt.Sprintf("https://%s.test:%d", hostname, c.HTTPSPort)
	}
	if c.HTTPPort == 80 {
		return fmt.Sprintf("http://%s.test", hostname)
	}
	return fmt.Sprintf("http://%s.test:%d", hostname, c.HTTPPort)
}


func (c GlobalConfig) ServiceURL(subdomain, hostname string) string {
	if c.TLS {
		if c.HTTPSPort == 443 {
			return fmt.Sprintf("https://%s.%s.test", subdomain, hostname)
		}
		return fmt.Sprintf("https://%s.%s.test:%d", subdomain, hostname, c.HTTPSPort)
	}
	if c.HTTPPort == 80 {
		return fmt.Sprintf("http://%s.%s.test", subdomain, hostname)
	}
	return fmt.Sprintf("http://%s.%s.test:%d", subdomain, hostname, c.HTTPPort)
}

func WithPorts(httpPort, httpsPort int, tls bool) GlobalConfig {
	return GlobalConfig{
		HTTPPort:  httpPort,
		HTTPSPort: httpsPort,
		TLS:       tls,
	}
}
