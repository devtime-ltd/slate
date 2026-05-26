package scaffold

import (
	"embed"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/templates"
)

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

	if len(aptPkgs) == 0 && len(phpExts) == 0 {
		return content, nil
	}

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

	// Insert before WORKDIR (which precedes USER) so apt/pecl run as root
	marker := "WORKDIR /app"
	if idx := strings.Index(content, marker); idx >= 0 {
		return content[:idx] + inject.String() + "\n" + content[idx:], nil
	}

	return content + inject.String(), nil
}

func (s *laravelScaffold) DefaultEnv(hostname string, globalCfg config.GlobalConfig) map[string]string {
	return map[string]string{
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
