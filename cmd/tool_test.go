package cmd

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/devtime-ltd/slate/internal/config"
)

func TestRunExecPlanStopsAtTheFirstFailure(t *testing.T) {
	plan := []config.ExecStep{
		{Service: "app", Command: []string{"one"}},
		{Service: "vite", Command: []string{"two"}},
		{Service: "app", Command: []string{"three"}},
	}
	var ran []string
	err := runExecPlan(plan, io.Discard, func(service string, command ...string) error {
		ran = append(ran, service+":"+strings.Join(command, " "))
		if command[0] == "two" {
			return errors.New("exit status 3")
		}
		return nil
	})

	if got := strings.Join(ran, ","); got != "app:one,vite:two" {
		t.Errorf("ran %q, want the first two steps only", got)
	}
	if err == nil || !strings.Contains(err.Error(), "step 2 of 3 (vite) failed: exit status 3") {
		t.Errorf("error = %v, want it to name the failed step", err)
	}
}

func TestRunExecPlanRunsEveryStepInOrder(t *testing.T) {
	plan := []config.ExecStep{
		{Service: "app", Command: []string{"./typegen-php.sh"}},
		{Service: "vite", Command: []string{"npm", "run", "extract"}},
	}
	var ran []string
	var progress strings.Builder
	err := runExecPlan(plan, &progress, func(service string, command ...string) error {
		ran = append(ran, service)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ran, ","); got != "app,vite" {
		t.Errorf("ran %q, want app,vite", got)
	}
	for _, want := range []string{"[1/2] app: ./typegen-php.sh", "[2/2] vite: npm run extract"} {
		if !strings.Contains(progress.String(), want) {
			t.Errorf("progress missing %q:\n%s", want, progress.String())
		}
	}
}

func TestRunExecPlanSingleCommandIsSilent(t *testing.T) {
	var progress strings.Builder
	wantErr := errors.New("exit status 1")
	err := runExecPlan([]config.ExecStep{{Service: "app", Command: []string{"composer"}}}, &progress, func(string, ...string) error {
		return wantErr
	})
	if err != wantErr {
		t.Errorf("a single command's error should pass through untouched, got %v", err)
	}
	if progress.Len() != 0 {
		t.Errorf("a single command should print no step header, got %q", progress.String())
	}
}

func TestExecToolSummary(t *testing.T) {
	single := config.ExecTool{Service: "app", Command: []string{"composer"}}
	if got := execToolSummary("composer", single); got != "Run composer in the app container" {
		t.Errorf("single summary = %q", got)
	}
	steps := config.ExecTool{Steps: []config.ExecStep{
		{Service: "app", Command: []string{"a"}},
		{Service: "vite", Command: []string{"b"}},
		{Service: "app", Command: []string{"c"}},
	}}
	if got := execToolSummary("typegen", steps); got != "Run typegen across the app and vite containers" {
		t.Errorf("steps summary = %q", got)
	}
}
