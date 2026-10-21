package scaffold

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/scaffolds"
)

const laravelPHPImage = "php:8.5-apache"

const (
	laravelDefaultPHPVersion = "8.5.12"
	laravelDefaultPHPSHA256  = "08bc2eba2a588654ade1357174aa646b761556c862dda0678345ee29ed481209"
)

var (
	phpReleasesURL    = "https://www.php.net/releases/index.php?json&version="
	phpReleaseTimeout = 10 * time.Second
	phpVersionRef     = regexp.MustCompile(`^(\d+\.\d+)(\.\d+)?$`)
	sha256Hex         = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type phpVersion struct {
	minor   string
	release string
}

func (v phpVersion) image() string { return "php:" + v.minor + "-apache" }

func (v phpVersion) atLeast(major, minor int) bool {
	var gotMajor, gotMinor int
	fmt.Sscanf(v.minor, "%d.%d", &gotMajor, &gotMinor)
	return gotMajor > major || (gotMajor == major && gotMinor >= minor)
}

func laravelPHPVersion(cfg config.ProjectConfig) (phpVersion, error) {
	raw, ok := cfg.Extra["php_version"]
	if !ok {
		return parsePHPVersion(laravelDefaultPHPVersion)
	}
	s, isString := raw.(string)
	if !isString {
		return phpVersion{}, fmt.Errorf("php_version must be a quoted string such as %q, got %T", laravelDefaultPHPVersion, raw)
	}
	return parsePHPVersion(s)
}

func parsePHPVersion(s string) (phpVersion, error) {
	m := phpVersionRef.FindStringSubmatch(s)
	if m == nil {
		return phpVersion{}, fmt.Errorf("php_version %q is not a PHP version: X.Y tracks the official image, X.Y.Z builds that release on top of it", s)
	}
	v := phpVersion{minor: m[1]}
	if m[2] != "" {
		v.release = s
	}
	return v, nil
}

// A release's sha256 is fetched once per machine and kept in a file of its
// own, written whole and renamed into place, so concurrent renders never race
// on a shared cache and a pinned workspace renders offline after its first
// build.
func phpSourceSHA256(version string) (string, error) {
	if version == laravelDefaultPHPVersion {
		return laravelDefaultPHPSHA256, nil
	}
	cachePath := filepath.Join(config.DataDir(), "php-releases", version+".sha256")
	if data, err := os.ReadFile(cachePath); err == nil {
		if sha := strings.TrimSpace(string(data)); sha256Hex.MatchString(sha) {
			return sha, nil
		}
	}
	sha, err := fetchPHPSourceSHA256(version)
	if err != nil {
		return "", err
	}
	cacheSHA256(cachePath, sha)
	return sha, nil
}

func cacheSHA256(path, sha string) {
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return
	}
	_, writeErr := tmp.WriteString(sha + "\n")
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil || os.Rename(tmp.Name(), path) != nil {
		_ = os.Remove(tmp.Name())
	}
}

func fetchPHPSourceSHA256(version string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), phpReleaseTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, phpReleasesURL+version, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "slate")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("looking up PHP %s on php.net: %w", version, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("looking up PHP %s on php.net: %s", version, resp.Status)
	}
	var release struct {
		Source []struct {
			Filename string `json:"filename"`
			SHA256   string `json:"sha256"`
		} `json:"source"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("reading php.net's listing for PHP %s: %w", version, err)
	}
	want := "php-" + version + ".tar.xz"
	for _, s := range release.Source {
		if s.Filename == want && sha256Hex.MatchString(s.SHA256) {
			return s.SHA256, nil
		}
	}
	return "", fmt.Errorf("php.net lists no release %s: php_version takes a released version such as %s", version, laravelDefaultPHPVersion)
}

func renderPHPSourceBuild(version, sha string) (string, error) {
	raw, err := scaffolds.Laravel.ReadFile("laravel/php-source.tmpl")
	if err != nil {
		return "", err
	}
	tmpl, err := template.New("php-source").Parse(string(raw))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, map[string]string{"Version": version, "SHA256": sha}); err != nil {
		return "", err
	}
	return b.String(), nil
}

func insertAfterLine(content, line, block string) string {
	if idx := strings.Index(content, line+"\n"); idx >= 0 {
		end := idx + len(line) + 1
		return content[:end] + block + content[end:]
	}
	return block + content
}
