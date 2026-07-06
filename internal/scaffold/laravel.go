package scaffold

import (
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/templates"
)

// laravelDefaultPHPIni is baked into every laravel workspace as
// /usr/local/etc/php/conf.d/10-slate-defaults.ini. The official php image
// ships no active php.ini, so without this every PHP process (CLI, queue,
// PHPUnit/Pest) gets the compiled-in memory_limit=128M default, which is
// not enough for real Laravel test suites and seeders.
//
// We deliberately do NOT set max_execution_time here: PHP's CLI SAPI
// defaults to 0 (unlimited), which is right for queue workers and tests;
// imposing a limit in conf.d would silently tighten that for everyone.
var laravelDefaultPHPIni = map[string]string{
	"memory_limit":        "512M",
	"upload_max_filesize": "100M",
	"post_max_size":       "100M",
}

type laravelScaffold struct{}

func init() {
	Register(&laravelScaffold{})
}

func (s *laravelScaffold) Name() string { return "laravel" }

func (s *laravelScaffold) FS() embed.FS { return templates.Laravel }

func (s *laravelScaffold) FileMap(slateDir string) map[string]string {
	return map[string]string{
		"compose.yaml.tmpl":     filepath.Join(slateDir, "compose.yaml"),
		"Dockerfile.tmpl":       filepath.Join(slateDir, "Dockerfile"),
		"000-default.conf.tmpl": filepath.Join(slateDir, "000-default.conf"),
		"dockerignore.tmpl":     filepath.Join(slateDir, ".dockerignore"),
	}
}

func (s *laravelScaffold) RenderDockerfile(content string, cfg config.ProjectConfig) (string, error) {
	aptPkgs := cfg.StringSlice("apt_packages")
	phpExts := cfg.StringSlice("php_extensions")
	phpIni := cfg.StringMap("php_ini")

	var inject strings.Builder

	if len(aptPkgs) > 0 {
		inject.WriteString("\nRUN apt-get update && apt-get install -y --no-install-recommends \\\n")
		for _, pkg := range aptPkgs {
			inject.WriteString(fmt.Sprintf("        %s \\\n", pkg))
		}
		inject.WriteString("    && rm -rf /var/lib/apt/lists/*\n")
	}

	if len(phpExts) > 0 {
		for _, ext := range phpExts {
			if isPeclExtension(ext) {
				inject.WriteString(fmt.Sprintf("RUN pecl install %s && docker-php-ext-enable %s\n", ext, ext))
			} else {
				inject.WriteString(fmt.Sprintf("RUN docker-php-ext-install %s\n", ext))
			}
		}
	}

	inject.WriteString(renderPHPIniDrop("10-slate-defaults.ini", laravelDefaultPHPIni))
	if len(phpIni) > 0 {
		inject.WriteString(renderPHPIniDrop("50-slate-yml.ini", phpIni))
	}

	// Insert before WORKDIR (which precedes USER) so apt/pecl/ini writes run as root
	marker := "WORKDIR /app"
	if idx := strings.Index(content, marker); idx >= 0 {
		content = content[:idx] + inject.String() + "\n" + content[idx:]
	} else {
		content += inject.String()
	}

	if cfg.AgentEnabled() {
		content += agentInstallBlock("www-data", "/var/www")
	}

	return content, nil
}

// renderPHPIniDrop emits a Dockerfile RUN that writes a php.ini conf.d
// drop-in. Keys are sorted for deterministic output. Both key and value
// are single-quoted and have any embedded single quotes escaped so that
// a hostile slate.yml can't break out of the quoting to inject shell
// during image build.
func renderPHPIniDrop(filename string, values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("RUN { \\\n")
	for _, k := range keys {
		safeK := shellSingleQuote(k)
		safeV := shellSingleQuote(values[k])
		b.WriteString(fmt.Sprintf("        printf '%%s = %%s\\n' %s %s; \\\n", safeK, safeV))
	}
	b.WriteString(fmt.Sprintf("    } > /usr/local/etc/php/conf.d/%s\n", filename))
	return b.String()
}

// shellSingleQuote wraps s in single quotes, escaping any embedded single
// quotes via the standard '\” idiom. Safe to pass any string through as
// a single shell-word argument.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (s *laravelScaffold) DefaultEnv(hostname string, globalCfg config.GlobalConfig) map[string]string {
	return map[string]string{
		"APP_KEY":             "{{GEN_APP_KEY}}",
		"APP_URL":             globalCfg.WorkspaceURL(hostname),
		"DB_CONNECTION":       "mysql",
		"DB_HOST":             "mysql",
		"DB_PORT":             "3306",
		"DB_DATABASE":         "{{DB_NAME:default}}",
		"DB_USERNAME":         "root",
		"DB_PASSWORD":         "{{GEN_PASSWORD:mysql}}",
		"MAIL_MAILER":         "smtp",
		"MAIL_HOST":           "mailpit",
		"MAIL_PORT":           "1025",
		"VITE_DEV_SERVER_URL": globalCfg.ServiceURL("vite", hostname),
	}
}

func (s *laravelScaffold) DefaultFiles() map[string]string {
	return map[string]string{
		"~/.composer/auth.json": "/app/.slate/composer/auth.json",
	}
}

func (s *laravelScaffold) Subdomains() map[string]Subdomain {
	return map[string]Subdomain{
		"":        {Service: "app", Port: 8080},
		"vite":    {Service: "vite", Port: 5173},
		"mailpit": {Service: "mailpit", Port: 8025},
	}
}

func (s *laravelScaffold) AppLikeServices() []string { return []string{"app", "queue"} }

func (s *laravelScaffold) Tools() map[string]config.Tool {
	return map[string]config.Tool{
		"composer": config.ExecTool{Service: "app", Command: []string{"composer"}},
		"artisan":  config.ExecTool{Service: "app", Command: []string{"php", "artisan"}},
		"pint":     config.ExecTool{Service: "app", Command: []string{"./vendor/bin/pint"}},
		"pest":     config.ExecTool{Service: "app", Command: []string{"./vendor/bin/pest"}},
		"npm":      config.ExecTool{Service: "vite", Command: []string{"npm"}},
		"npx":      config.ExecTool{Service: "vite", Command: []string{"npx"}},
		"tinker":   config.ExecTool{Service: "app", Command: []string{"php", "artisan", "tinker"}},
		"mysql":    config.DBTool{Service: "mysql", Port: 3306, Scheme: "mysql", User: "root", PasswordSalt: "mysql"},
	}
}

func isPeclExtension(ext string) bool {
	pecl := map[string]bool{"imagick": true, "redis": true, "xdebug": true, "apcu": true}
	return pecl[ext]
}
