package main

import (
	"flag"
	"os/exec"
	"strings"
	"testing"
)

func testFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("warpgate", flag.ContinueOnError)
	var cfg Config
	var s1, s2 string
	registerFlags(fs, &cfg, &s1, &s2)
	return fs
}

func TestFlagValueKindsMatchFlags(t *testing.T) {
	fs := testFlagSet()
	if _, err := completionFlags(fs); err != nil {
		t.Fatal(err)
	}
	for name := range flagValueKinds {
		if fs.Lookup(name) == nil {
			t.Errorf("flagValueKinds lists unknown flag -%s", name)
		}
	}
}

func TestCompletionScriptUnknownShell(t *testing.T) {
	if _, err := completionScript(testFlagSet(), "fish"); err == nil {
		t.Fatal("want an error for fish")
	}
}

// runShell runs setup, then script, then snippet in shell and returns the output.
func runShell(t *testing.T, shell, setup, script, snippet string) string {
	t.Helper()
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("%s not installed", shell)
	}
	out, err := exec.Command(shell, "-c", setup+"\n"+script+"\n"+snippet).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", shell, err, out)
	}
	return string(out)
}

func TestBashCompletion(t *testing.T) {
	script, err := completionScript(testFlagSet(), "bash")
	if err != nil {
		t.Fatal(err)
	}
	complete := func(words ...string) string {
		snippet := `COMP_WORDS=(` + strings.Join(words, " ") + `); COMP_CWORD=$((${#COMP_WORDS[@]}-1))
_warpgate; printf '%s\n' "${COMPREPLY[@]}"`
		return strings.TrimSpace(runShell(t, "bash", "", script, snippet))
	}
	if got := complete("warpgate", "-ul-"); got != "-ul-flat\n-ul-root" {
		t.Errorf("flag names: got %q", got)
	}
	if got := complete("warpgate", "-completion", "''"); got != "bash\nzsh" {
		t.Errorf("-completion values: got %q", got)
	}
	if got := complete("warpgate", "-listen", "''"); got != "" {
		t.Errorf("-listen: got %q, want nothing", got)
	}
	if got := complete("warpgate", "--root", "/"); !strings.Contains(got, "/tmp") || strings.Contains(got, "/etc/passwd") {
		t.Errorf("--root: got %q, want directories only", got)
	}
}

func TestZshCompletionParses(t *testing.T) {
	script, err := completionScript(testFlagSet(), "zsh")
	if err != nil {
		t.Fatal(err)
	}
	got := runShell(t, "zsh", "autoload -Uz compinit && compinit -u -D", script, `print -r -- $_comps[warpgate]`)
	if strings.TrimSpace(got) != "_warpgate" {
		t.Errorf("compdef not registered: %q", got)
	}
}
