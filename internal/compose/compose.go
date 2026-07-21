package compose

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/devtime-ltd/slate/internal/config"
	"github.com/devtime-ltd/slate/internal/scaffold"
)

type Env struct {
	MainRoot       string
	WorkspaceDir   string
	WorkspaceName  string
	ComposeProject string
}

func buildCmd(env Env, interactive bool, args ...string) *exec.Cmd {
	return buildComposeCmd(env, interactive, true, args...)
}

func buildComposeCmd(env Env, interactive, withOverride bool, args ...string) *exec.Cmd {
	uid, gid := "1000", "1000"
	if runtime.GOOS != "darwin" {
		uid = fmt.Sprintf("%d", os.Getuid())
		gid = fmt.Sprintf("%d", os.Getgid())
	}

	composeFile := scaffold.ComposeFilePath(env.WorkspaceDir)
	filesOverride := filepath.Join(env.WorkspaceDir, ".slate", "compose.files.yaml")

	cmdArgs := []string{"compose", "-f", composeFile}
	if _, err := os.Stat(filesOverride); withOverride && err == nil {
		cmdArgs = append(cmdArgs, "-f", filesOverride)
	}
	// only when present: down/rm must work on half-provisioned workspaces
	if envFile := filepath.Join(env.WorkspaceDir, ".env.container"); fileStatOK(envFile) {
		cmdArgs = append(cmdArgs, "--env-file", envFile)
	}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command("docker", cmdArgs...)
	cmd.Dir = env.WorkspaceDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if interactive {
		cmd.Stdin = os.Stdin
	}
	cmd.Env = append(os.Environ(),
		"MAIN_ROOT="+env.MainRoot,
		"APP_UID="+uid,
		"APP_GID="+gid,
		"SLATE_ENTRYPOINT="+config.DataDir()+"/entrypoint.sh",
		"COMPOSE_PROJECT_NAME="+env.ComposeProject,
	)

	return cmd
}

func fileStatOK(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func Run(env Env, args ...string) error {
	return buildCmd(env, false, args...).Run()
}

// DownProject tears down a compose project by name alone, resolving resources
// via their com.docker.compose.project labels. This works without the compose
// file, for workspaces whose directory no longer exists.
func DownProject(project string, args ...string) error {
	cmdArgs := append([]string{"compose", "-p", project, "down"}, args...)
	cmd := exec.Command("docker", cmdArgs...)
	// Compose falls back to any compose.yaml found in the CWD (or its
	// parents), which would scope `down -v` to that file's resources. Run
	// from an empty dir so it resolves purely by project label.
	if dir, err := os.MkdirTemp("", "slate-down-"); err == nil {
		defer os.RemoveAll(dir)
		cmd.Dir = dir
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func RunInteractive(env Env, args ...string) error {
	return buildCmd(env, true, args...).Run()
}

func Exec(env Env, service string, command ...string) error {
	args := []string{"exec", "-T", service}
	args = append(args, command...)
	return Run(env, args...)
}

// ExecPiped is like Exec (no TTY) but forwards stdin, so piped input works.
func ExecPiped(env Env, service string, command ...string) error {
	args := []string{"exec", "-T", service}
	args = append(args, command...)
	return buildCmd(env, true, args...).Run()
}

// AppLikeServices derives the services that bind-mount /app ("app" first) via
// `docker compose config`, which flattens anchors, merges, and interpolation.
// Base compose file only: a stale compose.files.yaml from a previous run could
// break config before GenerateFileMounts regenerates it.
func AppLikeServices(env Env) ([]string, error) {
	cmd := buildComposeCmd(env, false, false, "config", "--format", "json")
	cmd.Stdout = nil
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("resolving compose config: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("resolving compose config: %w", err)
	}
	return appLikeFromConfigJSON(out)
}

func appLikeFromConfigJSON(data []byte) ([]string, error) {
	var doc struct {
		Services map[string]struct {
			Volumes []struct {
				Type   string `json:"type"`
				Target string `json:"target"`
			} `json:"volumes"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing compose config: %w", err)
	}

	var names []string
	for name, svc := range doc.Services {
		for _, v := range svc.Volumes {
			if v.Type == "bind" && v.Target == "/app" {
				names = append(names, name)
				break
			}
		}
	}
	sort.Strings(names)
	for i, n := range names {
		if n == "app" && i != 0 {
			copy(names[1:i+1], names[:i])
			names[0] = "app"
			break
		}
	}
	return names, nil
}

func Port(env Env, service string, containerPort int) (string, error) {
	cmd := buildCmd(env, false, "port", service, fmt.Sprintf("%d", containerPort))
	cmd.Stdout = nil
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	// Handle IPv6: skip [::] lines, parse last colon for port
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(line, "[::]:") {
			continue
		}
		lastColon := strings.LastIndex(line, ":")
		if lastColon >= 0 {
			return line[lastColon+1:], nil
		}
	}
	return "", fmt.Errorf("could not parse port from: %s", out)
}
