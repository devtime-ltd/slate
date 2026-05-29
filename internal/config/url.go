package config

import "fmt"

func (c GlobalConfig) WorkspaceURL(hostname string) string {
	return c.urlFor(hostname + ".test")
}

func (c GlobalConfig) ServiceURL(subdomain, hostname string) string {
	return c.urlFor(subdomain + "." + hostname + ".test")
}

func (c GlobalConfig) urlFor(host string) string {
	if c.TLS {
		if c.HTTPSPort == 443 {
			return fmt.Sprintf("https://%s", host)
		}
		return fmt.Sprintf("https://%s:%d", host, c.HTTPSPort)
	}
	if c.HTTPPort == 80 {
		return fmt.Sprintf("http://%s", host)
	}
	return fmt.Sprintf("http://%s:%d", host, c.HTTPPort)
}

func WithPorts(httpPort, httpsPort int, tls bool) GlobalConfig {
	return GlobalConfig{
		HTTPPort:  httpPort,
		HTTPSPort: httpsPort,
		TLS:       tls,
	}
}
