package scaffold

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/scaffolds"
)

func laravelDockerfile(t *testing.T, cfg config.ProjectConfig) (string, error) {
	t.Helper()
	s, err := Get("laravel")
	if err != nil {
		t.Fatalf("Get(laravel) failed: %v", err)
	}
	raw, err := scaffolds.Laravel.ReadFile("laravel/Dockerfile.tmpl")
	if err != nil {
		t.Fatalf("reading embedded laravel Dockerfile: %v", err)
	}
	return s.(embeddedScaffold).RenderDockerfile(string(raw), cfg)
}

func laravelCfg(extra map[string]any) config.ProjectConfig {
	return config.ProjectConfig{Scaffold: config.ScaffoldRef{Name: "laravel"}, Extra: extra}
}

func TestLaravelDockerfileDefaultsToPinnedPHP(t *testing.T) {
	raw, _ := scaffolds.Laravel.ReadFile("laravel/Dockerfile.tmpl")
	if !strings.Contains(string(raw), "\nFROM "+laravelPHPImage+"\n") {
		t.Fatalf("laravelPHPImage must match the template's FROM line, else php_version no-ops")
	}
	if !strings.HasPrefix(laravelDefaultPHPVersion, strings.TrimSuffix(strings.TrimPrefix(laravelPHPImage, "php:"), "-apache")+".") {
		t.Fatalf("default %s is not a release of the template image %s", laravelDefaultPHPVersion, laravelPHPImage)
	}

	out, err := laravelDockerfile(t, laravelCfg(nil))
	if err != nil {
		t.Fatal(err)
	}
	fromEnd := strings.Index(out, "FROM "+laravelPHPImage+"\n") + len("FROM "+laravelPHPImage+"\n")
	if fromEnd < len("FROM "+laravelPHPImage+"\n") {
		t.Fatalf("FROM line missing:\n%s", out)
	}
	rest := out[fromEnd:]
	if !strings.HasPrefix(rest, "\nENV PHP_SOURCE_VERSION="+laravelDefaultPHPVersion+"\n") {
		t.Errorf("source build should follow the FROM line, pinned to %s:\n%s", laravelDefaultPHPVersion, out[:600])
	}
	if !strings.Contains(out, `ENV PHP_SOURCE_SHA256="`+laravelDefaultPHPSHA256+`"`) {
		t.Errorf("default sha256 missing:\n%s", out[:600])
	}
	if !strings.Contains(out, `dpkg --compare-versions "$(php -r 'echo PHP_VERSION;')" ge "$PHP_SOURCE_VERSION"; then exit 0; fi`) {
		t.Errorf("source build must skip itself once the image carries the release")
	}
	if !strings.Contains(out, "--with-apxs2") {
		t.Errorf("source build must produce the apache module the image serves through")
	}
	if strings.Index(out, "ENV PHP_SOURCE_VERSION") > strings.Index(out, "apt-get install") {
		t.Errorf("source build must precede the scaffold's apt and extension steps")
	}
}

func TestLaravelDockerfilePHPVersionMinorTracksImage(t *testing.T) {
	out, err := laravelDockerfile(t, laravelCfg(map[string]any{"php_version": "8.4"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\nFROM php:8.4-apache\n") {
		t.Errorf("php_version 8.4 should select php:8.4-apache:\n%s", out[:200])
	}
	if strings.Contains(out, "PHP_SOURCE_VERSION") {
		t.Errorf("an X.Y php_version must not add a source build")
	}
}

func TestLaravelDockerfileInstallsOPcacheOnlyBeforePHP85(t *testing.T) {
	for version, want := range map[string]bool{"8.3": true, "8.4.27": true, "8.5": false, "8.6": false, "9.0": false} {
		cfg := laravelCfg(map[string]any{"php_version": version})
		if version == "8.4.27" {
			t.Setenv("SLATE_DATA_DIR", t.TempDir())
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"source":[{"filename":"php-8.4.27.tar.xz","sha256":"dd54693aa334c5c4d72e9aee96b082c2473750a18a6c1d105789c93a3b1692fd"}]}`))
			}))
			prev := phpReleasesURL
			phpReleasesURL = srv.URL + "/?json&version="
			defer func() { phpReleasesURL = prev; srv.Close() }()
		}
		out, err := laravelDockerfile(t, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out, "        opcache \\\n"); got != want {
			t.Errorf("php_version %s: opcache in the ext-install list = %v, want %v", version, got, want)
		}
		if !strings.Contains(out, "        pcntl \\\n") {
			t.Errorf("php_version %s: the rest of the ext-install list must survive", version)
		}
	}
}

func TestLaravelDockerfilePHPVersionReleaseLooksUpAndCachesSHA(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SLATE_DATA_DIR", dataDir)
	const sha = "dd54693aa334c5c4d72e9aee96b082c2473750a18a6c1d105789c93a3b1692fd"
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Query().Get("version") != "8.3.36" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"version":"8.3.36","source":[
			{"filename":"php-8.3.36.tar.gz","sha256":"0000000000000000000000000000000000000000000000000000000000000000"},
			{"filename":"php-8.3.36.tar.xz","sha256":"` + sha + `"}]}`))
	}))
	defer srv.Close()
	prev := phpReleasesURL
	phpReleasesURL = srv.URL + "/?json&version="
	defer func() { phpReleasesURL = prev }()

	cfg := laravelCfg(map[string]any{"php_version": "8.3.36"})
	out, err := laravelDockerfile(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\nFROM php:8.3-apache\n") {
		t.Errorf("php_version 8.3.36 should select php:8.3-apache:\n%s", out[:200])
	}
	if !strings.Contains(out, "ENV PHP_SOURCE_VERSION=8.3.36\n") || !strings.Contains(out, `ENV PHP_SOURCE_SHA256="`+sha+`"`) {
		t.Errorf("source build should pin 8.3.36 with the tar.xz sha from php.net:\n%s", out[:600])
	}

	if data, err := os.ReadFile(filepath.Join(dataDir, "php-releases", "8.3.36.sha256")); err != nil || strings.TrimSpace(string(data)) != sha {
		t.Errorf("sha should be cached in its own file, got %q, %v", data, err)
	}

	srv.Close()
	if _, err := laravelDockerfile(t, cfg); err != nil {
		t.Errorf("second render should come from the cache, got %v", err)
	}
	if hits != 1 {
		t.Errorf("php.net asked %d times, want once", hits)
	}
}

func TestLaravelDockerfilePHPVersionUnknownRelease(t *testing.T) {
	t.Setenv("SLATE_DATA_DIR", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"source":[{"filename":"php-8.3.36.tar.xz","sha256":"dd54693aa334c5c4d72e9aee96b082c2473750a18a6c1d105789c93a3b1692fd"}]}`))
	}))
	defer srv.Close()
	prev := phpReleasesURL
	phpReleasesURL = srv.URL + "/?json&version="
	defer func() { phpReleasesURL = prev }()

	_, err := laravelDockerfile(t, laravelCfg(map[string]any{"php_version": "8.3.99"}))
	if err == nil || !strings.Contains(err.Error(), "no release 8.3.99") {
		t.Errorf("a version php.net does not list must fail the render, got %v", err)
	}
}

func TestLaravelDockerfilePHPVersionRejectsUnsafeValues(t *testing.T) {
	for _, bad := range []any{"8", "latest", "8.5.x", "8.5.12-rc1", "8.5.12; touch /pwned", "php:8.5-apache", 8.5, 8} {
		if _, err := laravelDockerfile(t, laravelCfg(map[string]any{"php_version": bad})); err == nil {
			t.Errorf("php_version %v should be rejected", bad)
		}
	}
}
