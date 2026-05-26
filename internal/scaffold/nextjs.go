package scaffold

import (
	"embed"
	"path/filepath"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/templates"
)

type nextjsScaffold struct{}

func init() {
	Register(&nextjsScaffold{})
}

func (s *nextjsScaffold) Name() string { return "nextjs" }

func (s *nextjsScaffold) FS() embed.FS { return templates.NextJS }

func (s *nextjsScaffold) FileMap(slateDir string) map[string]string {
	return map[string]string{
		"compose.yaml.tmpl": filepath.Join(slateDir, "compose.yaml"),
		"Dockerfile.tmpl":   filepath.Join(slateDir, "Dockerfile"),
		"dockerignore.tmpl": filepath.Join(slateDir, ".dockerignore"),
		"slate.yml.tmpl":    "",
	}
}

func (s *nextjsScaffold) DefaultEnv(hostname string, globalCfg config.GlobalConfig) map[string]string {
	return map[string]string{
		"APP_URL":      globalCfg.WorkspaceURL(hostname),
		"DATABASE_URL": "postgresql://app:{{GEN_PASSWORD:postgres}}@postgres:5432/{{DB_NAME:default}}",
		"MAIL_HOST":    "mailpit",
		"MAIL_PORT":    "1025",
	}
}

func (s *nextjsScaffold) DefaultFiles() map[string]string {
	return nil
}

func (s *nextjsScaffold) Tools() map[string]config.Tool {
	return map[string]config.Tool{
		"npm":    config.ExecTool{Service: "app", Command: []string{"npm"}},
		"npx":    config.ExecTool{Service: "app", Command: []string{"npx"}},
		"prisma": config.ExecTool{Service: "app", Command: []string{"npx", "prisma"}},
		"psql":   config.DBTool{Service: "postgres", Port: 5432, Scheme: "postgresql", User: "app", PasswordSalt: "postgres"},
	}
}

func (s *nextjsScaffold) RenderDockerfile(content string, cfg config.ProjectConfig) (string, error) {
	return content, nil
}
