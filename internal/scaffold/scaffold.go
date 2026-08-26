package scaffold

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/safeio"
)

type Scaffold interface {
	Name() string
	DefaultFiles() map[string]string                                             // host path -> container path
	DefaultEnv(hostname string, globalCfg config.GlobalConfig) map[string]string // default .env.container vars
	Tools() map[string]config.Tool                                               // exec + db tools
	// Subdomains maps subdomain prefix -> compose service name + container port.
	// Empty key "" is the main app at hostname.test; "vite" becomes vite.hostname.test, etc.
	Subdomains() map[string]Subdomain
	// AppLikeServices lists services that share the /app bind. First is the
	// primary; /app/* file mounts go on it alone (others would race). nil
	// means derive from the rendered compose file (inline scaffolds).
	AppLikeServices() []string
}

// embeddedScaffold adds the go:embed generation half only built-ins have.
type embeddedScaffold interface {
	Scaffold
	FS() embed.FS
	FileMap(slateDir string) map[string]string
	RenderDockerfile(content string, cfg config.ProjectConfig) (string, error)
}

type Subdomain struct {
	Service string
	Port    int
}

// Identity is the workspace naming trio, exposed to compose templates.
type Identity struct {
	Project   string
	Workspace string
	Hostname  string
}

var registry = map[string]Scaffold{}

func Register(s Scaffold) {
	registry[s.Name()] = s
}

func Get(name string) (Scaffold, error) {
	s, ok := registry[name]
	if !ok {
		names := make([]string, 0, len(registry))
		for n := range registry {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown scaffold: %s (available: %s, or an inline scaffold map; see README)", name, strings.Join(names, ", "))
	}
	return s, nil
}

// Resolve returns the scaffold slate.yml selects. No scaffold (and the legacy
// `none`) resolves to an empty inline one so config-defined tools still work.
func Resolve(cfg config.ProjectConfig) (Scaffold, error) {
	if cfg.Scaffold.Inline != nil {
		return &inlineScaffold{def: cfg.Scaffold.Inline}, nil
	}
	switch cfg.Scaffold.Name {
	case "", "none":
		return &inlineScaffold{def: &config.InlineScaffold{}}, nil
	}
	return Get(cfg.Scaffold.Name)
}

// Generate renders the scaffold's files and writes them into the workspace.
// Rendering is completed for every file before anything is written, so a
// rendering error (a bad template, an invalid node_image) leaves the workspace
// untouched.
func Generate(workspaceDir, mainRoot string, cfg config.ProjectConfig, id Identity) error {
	slateDir := filepath.Join(workspaceDir, ".slate")
	if err := os.MkdirAll(slateDir, 0o755); err != nil {
		return fmt.Errorf("creating .slate dir: %w", err)
	}
	// .slate is container-writable, so pin it as an fd and write the generated
	// files through *at syscalls: OpenDir refuses a symlinked .slate, and the
	// fd resolves against its inode so a concurrent swap of the path can't
	// redirect the writes to a host file.
	slateFd, err := safeio.OpenDir(slateDir)
	if err != nil {
		return fmt.Errorf("opening .slate: %w", err)
	}
	defer slateFd.Close()

	s, err := Resolve(cfg)
	if err != nil {
		return err
	}

	es, ok := s.(embeddedScaffold)
	if !ok {
		return generateInline(workspaceDir, mainRoot, cfg, id)
	}

	name := s.Name()
	fileMap := es.FileMap(slateDir)

	entries, err := es.FS().ReadDir(name)
	if err != nil {
		return fmt.Errorf("reading scaffold %s: %w", name, err)
	}

	type renderedFile struct {
		destPath string
		content  string
	}
	var files []renderedFile
	for _, entry := range entries {
		destPath, ok := fileMap[entry.Name()]
		if !ok || destPath == "" {
			continue
		}

		data, err := es.FS().ReadFile(name + "/" + entry.Name())
		if err != nil {
			return fmt.Errorf("reading %s: %w", entry.Name(), err)
		}

		content := string(data)

		if entry.Name() == "Dockerfile.tmpl" {
			content, err = es.RenderDockerfile(content, cfg)
			if err != nil {
				return fmt.Errorf("rendering Dockerfile: %w", err)
			}
		}

		if entry.Name() == "compose.yaml.tmpl" {
			content, err = renderCompose(content, mainRoot, cfg, id)
			if err != nil {
				return fmt.Errorf("rendering compose.yaml: %w", err)
			}
		}

		files = append(files, renderedFile{destPath, content})
	}

	for _, f := range files {
		// The writes go through the pinned .slate fd, so a scaffold whose
		// FileMap ever nests a file in a subdirectory would be silently
		// misplaced at the top level; require a direct child instead.
		if filepath.Dir(f.destPath) != slateDir {
			return fmt.Errorf("generated file %s is not directly under .slate", f.destPath)
		}
		if err := safeio.WriteFileAt(slateFd, filepath.Base(f.destPath), []byte(f.content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", f.destPath, err)
		}
	}

	return nil
}

const defaultNodeImage = "node:24"

// Deliberately permissive about structure (registry, port, path, tag, digest)
// and strict about the characters that would let a value break out of the
// compose field it lands in: whitespace, quotes, and ${...} interpolation.
var nodeImageRef = regexp.MustCompile(`^[A-Za-z0-9\[][A-Za-z0-9._:/@\[\]-]{0,999}$`)

func nodeImage(cfg config.ProjectConfig, fallback string) (string, error) {
	if raw, ok := cfg.Extra["node_image"]; ok {
		if _, isString := raw.(string); !isString {
			return "", fmt.Errorf("node_image must be a string, got %T", raw)
		}
	}
	img := cfg.String("node_image")
	if img == "" {
		return fallback, nil
	}
	if !nodeImageRef.MatchString(img) {
		return "", fmt.Errorf("node_image %q is not a valid image reference", img)
	}
	return img, nil
}

// renderCompose omits the ${MAIN_ROOT}/.env bind mount when the main checkout
// has no real .env file, otherwise Docker silently creates the missing source
// as a directory in the user's project. A directory at that path counts as
// absent so a leftover one is never re-mounted.
func renderCompose(content, mainRoot string, cfg config.ProjectConfig, id Identity) (string, error) {
	hasMainEnv := false
	if mainRoot != "" {
		if info, err := os.Stat(filepath.Join(mainRoot, ".env")); err == nil && !info.IsDir() {
			hasMainEnv = true
		}
	}

	var vars map[string]any
	if cfg.Scaffold.Inline != nil {
		vars = cfg.Scaffold.Inline.Vars
	}

	tmpl, err := template.New("compose").Parse(content)
	if err != nil {
		return "", err
	}
	img, err := nodeImage(cfg, defaultNodeImage)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, map[string]any{
		"HasMainEnv": hasMainEnv,
		"NodeImage":  img,
		"Database":   cfg.Database,
		"Project":    id.Project,
		"Workspace":  id.Workspace,
		"Hostname":   id.Hostname,
		"Vars":       vars,
	}); err != nil {
		return "", err
	}
	return b.String(), nil
}

func rootEnvHasValue(mainRoot, key string) bool {
	if mainRoot == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(mainRoot, ".env"))
	if err != nil {
		return false
	}
	prefix := key + "="
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)) != ""
		}
	}
	return false
}

func GenerateFileMounts(workspaceDir string, cfg config.ProjectConfig, s Scaffold, appLike []string) error {
	files := make(map[string]string)
	for k, v := range s.DefaultFiles() {
		files[k] = v
	}
	for k, v := range cfg.StringMap("files") {
		files[k] = v
	}

	slateDir := filepath.Join(workspaceDir, ".slate")

	if err := os.MkdirAll(slateDir, 0o755); err != nil {
		return fmt.Errorf("creating .slate dir: %w", err)
	}
	// .slate is container-writable; pin it as an fd so the writes below go
	// through *at syscalls and a concurrent path swap can't redirect them.
	slateFd, err := safeio.OpenDir(slateDir)
	if err != nil {
		return fmt.Errorf("opening .slate: %w", err)
	}
	defer slateFd.Close()

	if err := safeio.RemoveAllAt(slateFd, "compose.files.yaml"); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not remove stale compose.files.yaml: %v\n", err)
	}

	if len(files) == 0 {
		return nil
	}

	// Clear and recreate .slate/files entirely through the pinned .slate fd, so
	// a container swapping a path component can't redirect the removal or write.
	if err := safeio.RemoveAllAt(slateFd, "files"); err != nil {
		return fmt.Errorf("clearing files dir: %w", err)
	}
	if err := safeio.MkdirAt(slateFd, "files", 0o755); err != nil {
		return fmt.Errorf("creating files dir: %w", err)
	}
	filesFd, err := safeio.OpenDirAt(slateFd, "files")
	if err != nil {
		return fmt.Errorf("opening .slate/files: %w", err)
	}
	defer filesFd.Close()

	services := appLike
	if len(services) == 0 {
		return fmt.Errorf("no services share the /app bind (scaffold %q); file mounts cannot be applied", s.Name())
	}
	primary := services[0]

	var sharedMounts, appOnlyMounts []string
	i := 0
	for source, target := range files {
		expanded := expandHome(source)
		data, err := os.ReadFile(expanded)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warning: skipping file mount %s -> %s (%v)\n", source, target, err)
			continue
		}

		localName := fmt.Sprintf("file_%d", i)
		if err := safeio.WriteFileAt(filesFd, localName, data, 0o644); err != nil {
			return fmt.Errorf("writing .slate/files/%s: %w", localName, err)
		}

		mount := fmt.Sprintf("      - ./files/%s:%s:ro", localName, target)
		if strings.HasPrefix(target, "/app/") {
			appOnlyMounts = append(appOnlyMounts, mount)
		} else {
			sharedMounts = append(sharedMounts, mount)
		}
		i++
	}

	if len(sharedMounts) == 0 && len(appOnlyMounts) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("services:\n")
	for _, svc := range services {
		if svc != primary && len(sharedMounts) == 0 {
			continue
		}
		b.WriteString(fmt.Sprintf("  %s:\n    volumes:\n", svc))
		for _, m := range sharedMounts {
			b.WriteString(m + "\n")
		}
		if svc == primary {
			for _, m := range appOnlyMounts {
				b.WriteString(m + "\n")
			}
		}
	}

	return safeio.WriteFileAt(slateFd, "compose.files.yaml", []byte(b.String()), 0o644)
}

var dbNameRe = regexp.MustCompile(`\{\{DB_NAME(?::([^}]*))?\}\}`)
var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func expandDBName(value, project, workspace string) string {
	return dbNameRe.ReplaceAllStringFunc(value, func(match string) string {
		sub := dbNameRe.FindStringSubmatch(match)
		label := "default"
		if len(sub) > 1 && sub[1] != "" {
			label = strings.TrimSpace(sub[1])
		}
		return DeriveDBName(project, workspace, label)
	})
}

func DeriveDBName(project, workspace, label string) string {
	ws := sanitiseDBSegment(workspace, 18)
	hash := shortHash(project, workspace, label)

	if label == "default" {
		return ensureLeadingAlpha(ws + "_" + hash)
	}

	lb := sanitiseDBSegment(label, 18)
	return ensureLeadingAlpha(ws + "_" + lb + "_" + hash)
}

func sanitiseDBSegment(s string, maxLen int) string {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if len(s) > maxLen {
		s = s[:maxLen]
		s = strings.TrimRight(s, "_")
	}
	return s
}

func ensureLeadingAlpha(s string) string {
	if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		return "x_" + s
	}
	return s
}

func shortHash(parts ...string) string {
	h := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(parts, ":"))))
	return h[:6]
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func ComposeFilePath(workspaceDir string) string {
	return filepath.Join(workspaceDir, ".slate", "compose.yaml")
}

// EnsureGitignore returns the entries it appended.
func EnsureGitignore(projectDir string) ([]string, error) {
	gi := filepath.Join(projectDir, ".gitignore")
	data, _ := os.ReadFile(gi)

	have := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		have[strings.TrimSpace(line)] = true
	}

	// Both slate artifacts live in the worktree: the .slate/ infra dir and the
	// generated .env.container at the worktree root. The latter is easy to miss
	// and otherwise shows up as an untracked file (and trips the `slate rm`
	// uncommitted-changes warning) on every workspace.
	var missing []string
	if !have[".slate"] && !have[".slate/"] && !gitIgnored(projectDir, ".slate/") {
		missing = append(missing, ".slate/")
	}
	if !have[".env.container"] && !gitIgnored(projectDir, ".env.container") {
		missing = append(missing, ".env.container")
	}
	// The per-developer overlay is only covered once it exists: a project
	// without one shouldn't carry the entry.
	if info, err := os.Stat(filepath.Join(projectDir, config.LocalConfigName)); err == nil && !info.IsDir() {
		if !have[config.LocalConfigName] && !gitIgnored(projectDir, config.LocalConfigName) {
			missing = append(missing, config.LocalConfigName)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}

	f, err := os.OpenFile(gi, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if len(data) > 0 && data[len(data)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return nil, err
		}
	}
	for _, entry := range missing {
		if _, err := f.WriteString(entry + "\n"); err != nil {
			return nil, err
		}
	}
	return missing, nil
}

// gitIgnored reports whether git already ignores path (any ignore source).
func gitIgnored(dir, path string) bool {
	return exec.Command("git", "-C", dir, "check-ignore", "-q", path).Run() == nil
}

func GenerateEnvContainer(workspaceDir, mainRoot, hostname, project, workspace string, cfg config.ProjectConfig, globalCfg config.GlobalConfig) error {
	defaults := make(map[string]string)

	// Get scaffold-specific defaults if available
	if s, err := Resolve(cfg); err == nil {
		for k, v := range s.DefaultEnv(hostname, globalCfg) {
			defaults[k] = v
		}
	}

	// .env.container is merged after the root .env and would override it, so
	// don't clobber a real APP_KEY the project already sets.
	if _, userSet := cfg.Env["APP_KEY"]; !userSet && rootEnvHasValue(mainRoot, "APP_KEY") {
		delete(defaults, "APP_KEY")
	}

	// User overrides from slate.yml
	for k, v := range cfg.Env {
		defaults[k] = v
	}

	// Expand placeholders
	for k, v := range defaults {
		v = expandDBName(v, project, workspace)
		v = config.ExpandPasswords(v, globalCfg.SecretKey, project, workspace)
		v = config.ExpandAppKey(v, globalCfg.SecretKey, project, workspace)
		defaults[k] = v
	}

	keys := make([]string, 0, len(defaults))
	for k := range defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, defaults[k])
	}

	dir, err := safeio.OpenDir(workspaceDir)
	if err != nil {
		return fmt.Errorf("opening workspace dir: %w", err)
	}
	defer dir.Close()
	return safeio.WriteFileAt(dir, ".env.container", []byte(b.String()), 0o644)
}

// BuildLifecycleScript assembles the full script for a lifecycle phase.
// slate new: setup (install deps + migrate) then fresh (fresh DB)
// slate up:  setup (install deps + migrate)
func BuildLifecycleScript(cfg config.ProjectConfig, isNew bool) string {
	var parts []string

	upScript := cfg.Setup
	defaultUp := config.DefaultSetupForScaffold(cfg.Scaffold.Name)
	if upScript == "" {
		upScript = defaultUp
	} else {
		upScript = strings.ReplaceAll(upScript, "{{SCAFFOLD_DEFAULT}}", defaultUp)
	}
	if upScript != "" {
		parts = append(parts, upScript)
	}

	if isNew {
		newScript := cfg.Fresh
		defaultNew := config.DefaultFreshSetupForScaffold(cfg.Scaffold.Name)
		if newScript == "" {
			newScript = defaultNew
		} else {
			newScript = strings.ReplaceAll(newScript, "{{SCAFFOLD_DEFAULT}}", defaultNew)
		}
		if newScript != "" {
			parts = append(parts, newScript)
		}
	}

	if len(parts) == 0 {
		return ""
	}

	script := "set -e\n" + retryShellFunc
	for _, p := range parts {
		script += strings.TrimRight(p, "\n") + "\n"
	}
	return script
}

// retryShellFunc defines a `retry` shell helper available to every lifecycle
// script: it runs a command up to 3 times with linear backoff, so transient
// network blips (e.g. flaky codeload.github.com 400s during composer/npm
// install) don't fail the whole provision. The final attempt runs bare so its
// exit status propagates under `set -e`.
const retryShellFunc = `retry() {
  n=1
  while [ "$n" -lt 3 ]; do
    "$@" && return 0
    echo "slate: '$1' failed (attempt $n/3); retrying in $((n*3))s..." >&2
    sleep $((n*3))
    n=$((n+1))
  done
  "$@"
}
`
