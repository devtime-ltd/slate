package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var waitTimeout time.Duration

var waitCmd = &cobra.Command{
	Use:   "wait [workspace]",
	Short: "Block until a workspace finishes provisioning",
	Long: `Blocks while a background provision (slate new/up --bg, or a configured
new: hook) is in flight, then exits 0 when the workspace is ready. Exits
non-zero, printing the tail of the provision log, when provisioning failed.
Returns immediately when nothing is in flight.`,
	GroupID: "workspace",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, wsDir, err := resolveNameOrCwd(args)
		if err != nil {
			return err
		}
		if err := awaitProvision(wsDir, waitTimeout); err != nil {
			return err
		}
		// Unlike the exec-path gate, an explicit wait also reports a provision
		// that already failed before we were called.
		if _, err := os.Stat(filepath.Join(wsDir, ".slate", "provisioning.failed")); err == nil {
			return provisionEndedErr(wsDir, "provisioning failed")
		}
		fmt.Println(tick() + " " + name + " ready")
		return nil
	},
}

func init() {
	waitCmd.Flags().DurationVar(&waitTimeout, "timeout", 0, "Give up after this long (0 = wait forever)")
	rootCmd.AddCommand(waitCmd)
}

// awaitProvision blocks while a background provisioner is running for wsDir
// and reports how a provision it watched ended: nil when it completed (or
// nothing was live), an error carrying the log tail when it failed or died.
// A .failed marker from an earlier run does NOT error here: exec-style
// callers gate on this, and running commands in a half-provisioned workspace
// to debug it is legitimate. Progress goes to stderr so those callers keep
// stdout clean.
func awaitProvision(wsDir string, timeout time.Duration) error {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	watched := false
	for {
		pid, alive := readProvisioningLock(wsDir)
		if pid == 0 {
			break
		}
		if !alive {
			return provisionEndedErr(wsDir, fmt.Sprintf("background provisioner (pid %d) died before finishing", pid))
		}
		if !watched {
			fmt.Fprintf(os.Stderr, "Provisioning in flight (pid %d), waiting...\n", pid)
			watched = true
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("still provisioning after %s (pid %d)\nLog: %s", timeout, pid, provisionLogPath(wsDir))
		}
		time.Sleep(500 * time.Millisecond)
	}
	if watched {
		if _, err := os.Stat(filepath.Join(wsDir, ".slate", "provisioning.failed")); err == nil {
			return provisionEndedErr(wsDir, "provisioning failed")
		}
	}
	return nil
}

func provisionLogPath(wsDir string) string {
	return filepath.Join(wsDir, ".slate", "provision.log")
}

// provisionEndedErr describes a provision that ended badly, with the log tail
// inline so the failure is diagnosable without another command.
func provisionEndedErr(wsDir, cause string) error {
	logPath := provisionLogPath(wsDir)
	msg := fmt.Sprintf("%s. Resume with `slate up %s`.\nLog: %s", cause, filepath.Base(wsDir), logPath)
	if tail := tailFile(logPath, 15); tail != "" {
		msg += "\n\n" + tail
	}
	return errors.New(msg)
}

// tailFile returns up to the last n lines of path, reading only the final
// chunk so a multi-megabyte provision log (docker build output) isn't
// slurped whole for an error message.
func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	const chunk = 64 * 1024
	off := info.Size() - chunk
	if off < 0 {
		off = 0
	}
	data := make([]byte, info.Size()-off)
	if _, err := f.ReadAt(data, off); err != nil && err != io.EOF {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
