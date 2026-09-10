package cmd

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// Without the hold the test binary dies here, as slate would mid-exec with
// the workers still paused.
func TestHoldInterruptsSurvivesSIGINT(t *testing.T) {
	release := holdInterrupts()
	defer release()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
}
