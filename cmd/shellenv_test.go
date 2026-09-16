package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devtime-ltd/slate/internal/assets"
)

// fakeSlate stands in for the binary on PATH, answering `where` for a fixed
// set of names and `__complete where` in cobra's output format.
const fakeSlate = `#!/bin/sh
cmd=$1; shift
[ "$1" = "--" ] && shift
case "$cmd" in
where)
	case "$1" in
	"") echo "$FAKE_ROOT" ;;
	sparta) echo "$FAKE_ROOT/sparta" ;;
	sparta@redis-cache) echo "$FAKE_ROOT/sparta/.slate/workspaces/redis-cache" ;;
	-frontend) echo "$FAKE_ROOT/-frontend" ;;
	rel) echo "-frontend" ;;
	*) echo "nothing called '$1'" >&2; exit 1 ;;
	esac ;;
__complete)
	shift
	[ "$1" = "--" ] && shift
	case "$1" in
	*@*) all="sparta@bulk-invite
sparta@redis-cache
foo bar@redis-cache" ;;
	*) all="4lun
centerframe/sparta
sparta
foo bar
foo baz
-frontend
foo\$bar
it's
foo\\qbar
:colon
:4
*
[x]
foo!bar" ;;
	esac
	printf '%s\n' "$all" | while IFS= read -r c; do case "$c" in "$1"*) printf '%s\n' "$c" ;; esac; done
	echo ":4" ;;
esac
`

func shellenvHarness(t *testing.T, shell string) ([]string, string) {
	t.Helper()
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("%s not installed", shell)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "slate"), []byte(fakeSlate), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(bin, "dev."+shell)
	if err := os.WriteFile(script, shellenvScripts[shell], 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, dir := range []string{filepath.Join("sparta", ".slate", "workspaces", "redis-cache"), "-frontend"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FAKE_ROOT="+root, "SCRIPT="+script, "DUMP="+filepath.Join(bin, "zcompdump"))
	return env, root
}

func runShell(t *testing.T, shell string, env []string, body string) []string {
	t.Helper()
	cmd := exec.Command(shell, "-c", body)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", shell, err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func expectLines(t *testing.T, got []string, want ...string) {
	t.Helper()
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %q in output:\n%s", w, strings.Join(got, "\n"))
		}
	}
}

func TestShellenvDevChangesDirectoryDespiteAnInheritedAlias(t *testing.T) {
	// non-interactive bash ignores aliases unless expand_aliases is on
	prelude := map[string]string{"bash": "shopt -s expand_aliases\n"}
	for _, shell := range []string{"zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			env, root := shellenvHarness(t, shell)
			out := runShell(t, shell, env, prelude[shell]+`
alias dev='cd /'
eval "$(cat "$SCRIPT")"
cd /
dev sparta && echo "1:$PWD"
dev sparta@redis-cache && echo "2:$PWD"
dev nope 2>/dev/null; echo "3:$? $PWD"
dev && echo "4:$PWD"
dev -frontend && echo "5:$PWD"
cd "$FAKE_ROOT" && dev rel && echo "6:$PWD"
`)
			ws := filepath.Join(root, "sparta", ".slate", "workspaces", "redis-cache")
			expectLines(t, out, "1:"+filepath.Join(root, "sparta"), "2:"+ws, "3:1 "+ws, "4:"+root, "5:"+filepath.Join(root, "-frontend"), "6:"+filepath.Join(root, "-frontend"))
		})
	}
}

func TestShellenvSurvivesErrexitWithNoAliasToRemove(t *testing.T) {
	for _, shell := range []string{"zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			env, _ := shellenvHarness(t, shell)
			out := runShell(t, shell, env, `
set -e
eval "$(cat "$SCRIPT")"
type dev >/dev/null && echo DEFINED
`)
			expectLines(t, out, "DEFINED")
		})
	}
}

func TestShellenvZshCompletesThroughCompctl(t *testing.T) {
	env, _ := shellenvHarness(t, "zsh")
	out := runShell(t, "zsh", env, `
eval "$(cat "$SCRIPT")"
_dev_compctl 'spa'; print -r -- "A:${(j:,:)reply}"
_dev_compctl 'sparta@r'; print -r -- "B:${(j:,:)reply}"
_dev_compctl 'nope@'; print -r -- "C:${#reply}"
_dev_compctl 'foo ba'; print -r -- "D:${(j:|:)reply}"
_dev_compctl '-fr'; print -r -- "E:${(j:|:)reply}"
_dev_compctl ':c'; print -r -- "G:${(j:|:)reply}"
_dev_compctl ':4'; print -r -- "H:${(j:|:)reply}"
_dev_compctl 'zzz'; print -r -- "I:${#reply}"
setopt GLOB_SUBST
_dev_compctl '*'; print -r -- "J:${(j:|:)reply}"
_dev_compctl '['; print -r -- "K:${(j:|:)reply}"
if compctl -L dev | grep -q 'p\[1'; then print -r -- "F:first-argument-only"; fi
`)
	expectLines(t, out, "A:sparta", "B:sparta@redis-cache", "C:0", "D:foo bar|foo baz", "E:-frontend", "F:first-argument-only", "G::colon", "H::4", "I:0", "J:*", "K:[x]")
}

func TestShellenvZshRegistersWithCompsysWhenLoaded(t *testing.T) {
	env, _ := shellenvHarness(t, "zsh")
	out := runShell(t, "zsh", env, `
autoload -Uz compinit; compinit -u -d "$DUMP"
eval "$(cat "$SCRIPT")"
print -r -- "R:${_comps[dev]}"
`)
	expectLines(t, out, "R:_dev")
}

func TestShellenvBashCompletesAcrossTheWordBreak(t *testing.T) {
	env, _ := shellenvHarness(t, "bash")
	out := runShell(t, "bash", env, `
eval "$(cat "$SCRIPT")"
try() { COMP_WORDBREAKS=$1; COMP_LINE=$2; COMP_POINT=${#2}; COMP_WORDS=(); COMP_CWORD=0; _dev_complete; echo "$3:${COMPREPLY[*]}"; }
at=$' \t\n"\'@><=;|&(:'
plain=$' \t\n"\'><=;|&(:'
try "$at" 'dev spa' A
try "$at" $'dev\tspa' A2
try "$at" 'dev sparta@r' B
try "$at" 'dev sparta@' C
try "$at" 'dev nope@' D
try "$at" 'dev foo\ ba' E
try "$at" 'dev foo\ bar@r' F
try "$plain" 'dev sparta@r' G
try "$plain" 'dev sparta@' H
try "$plain" 'dev foo\ bar@r' I
try "$at" 'dev -fr' J
try "$at" 'dev "foo b' K
try "$at" "dev 'foo bar@r" L
try "$at" 'dev "foo bar@' M
try "$at" 'dev foo" b' N
try "$at" 'dev "foo "b' O
try "$at" 'dev foo\ bar@r' P
try "$at" 'dev "foo "bar@r' Q
try "$at" 'dev "foo$' R
try "$at" 'dev foo$' S
try "$at" "dev 'it" T
try "$at" 'dev "foo\q' U
try "$at" 'dev "foo\$' V
try "$at" 'dev sparta ' W
try "$at" 'dev sparta sp' X
try "$at" 'dev "foo bar" ' Y
try "$at" 'dev :c' Z
try "$at" 'echo done; dev spa' AA
try "$at" 'true && dev spa' AB
try "$at" 'x | dev spa' AC
try "$at" '(dev spa' AD
try "$at" 'echo a b; dev sparta ' AE
try "$at" 'dev :4' AF
try "$at" 'dev [' AG
try "$at" 'dev 2>/tmp/log spa' AH
try "$at" 'dev > /tmp/log spa' AI
try "$at" 'dev 2>&1 spa' AJ
try "$at" 'dev </dev/null >> /tmp/log spa' AK
try "$at" 'dev sparta 2>&1 ' AL
try "$at" 'dev &>/dev/null spa' AM
try "$at" 'dev sparta &>/dev/null ' AN
try "$at" 'dev >&2 spa' AO
try "$at" 'true & dev spa' AP
try "$at" 'dev "foo!' AQ
`)
	expectLines(t, out,
		"A:sparta",
		"A2:sparta",
		"B:@redis-cache",
		"C:@bulk-invite @redis-cache",
		"D:",
		`E:foo\ bar foo\ baz`,
		"F:@redis-cache",
		"G:sparta@redis-cache",
		"H:sparta@bulk-invite sparta@redis-cache",
		`I:foo\ bar@redis-cache`,
		"J:-frontend",
		"K:foo bar foo baz",
		"L:foo bar@redis-cache",
		"M:foo bar@redis-cache",
		"N: bar  baz",
		`O:foo\ bar foo\ baz`,
		"P:@redis-cache",
		"Q:@redis-cache",
		`R:foo\$bar`,
		`S:foo\$bar`,
		`T:it'\''s`,
		`U:foo\\qbar`,
		`V:foo\$bar`,
		"W:",
		"X:",
		"Y:",
		"Z:colon",
		"AA:sparta",
		"AB:sparta",
		"AC:sparta",
		"AD:sparta",
		"AE:",
		"AF:4",
		`AG:\[x\]`,
		"AH:sparta",
		"AI:sparta",
		"AJ:sparta",
		"AK:sparta",
		"AL:",
		"AM:sparta",
		"AN:",
		"AO:sparta",
		"AP:sparta",
		`AQ:foo"'!'"bar`,
	)
}

func TestShellenvPrintsTheEmbeddedScript(t *testing.T) {
	var out bytes.Buffer
	shellenvCmd.SetOut(&out)
	defer shellenvCmd.SetOut(nil)
	if err := shellenvCmd.RunE(shellenvCmd, []string{"bash"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), assets.DevBash) {
		t.Error("bash output is not the embedded script")
	}
	if err := shellenvCmd.RunE(shellenvCmd, []string{"fish"}); err == nil || !strings.Contains(err.Error(), "fish") {
		t.Errorf("fish: err = %v, want it named", err)
	}
}
