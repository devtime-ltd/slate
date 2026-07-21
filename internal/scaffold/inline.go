package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/workspace"
)

type inlineScaffold struct {
	def *config.InlineScaffold
}

func (s *inlineScaffold) Name() string                    { return "inline" }
func (s *inlineScaffold) DefaultFiles() map[string]string { return nil }
func (s *inlineScaffold) Tools() map[string]config.Tool   { return nil }
func (s *inlineScaffold) AppLikeServices() []string       { return nil }

func (s *inlineScaffold) DefaultEnv(hostname string, globalCfg config.GlobalConfig) map[string]string {
	return nil
}

func (s *inlineScaffold) Subdomains() map[string]Subdomain {
	out := make(map[string]Subdomain, len(s.def.Subdomains))
	for prefix, sp := range s.def.Subdomains {
		out[prefix] = Subdomain{Service: sp.Service, Port: sp.Port}
	}
	return out
}

// generateInline sources .slate/compose.yaml from the file named in slate.yml.
// No compose path (including legacy `none`) generates nothing.
func generateInline(workspaceDir, mainRoot string, cfg config.ProjectConfig, id Identity) error {
	def := cfg.Scaffold.Inline
	if def == nil || def.Compose == "" {
		return nil
	}

	rel := filepath.Clean(def.Compose)
	if !filepath.IsLocal(rel) {
		return fmt.Errorf("scaffold compose path %q must be relative and stay inside the project", def.Compose)
	}

	// Trusted sources only: the branch's committed copy (containers cannot
	// commit), else the main checkout. Never the container-writable worktree.
	data, ok := workspace.CommittedFile(mainRoot, workspaceDir, rel)
	if !ok {
		var err error
		data, err = os.ReadFile(filepath.Join(mainRoot, rel))
		if err != nil {
			return fmt.Errorf("scaffold compose file %s: %w (commit it on this branch or add it to the main checkout)", def.Compose, err)
		}
	}

	content := string(data)
	if strings.HasSuffix(rel, ".tmpl") {
		rendered, err := renderCompose(content, mainRoot, cfg, id)
		if err != nil {
			return fmt.Errorf("rendering %s: %w", def.Compose, err)
		}
		content = rendered
	}

	return os.WriteFile(ComposeFilePath(workspaceDir), []byte(content), 0o644)
}
