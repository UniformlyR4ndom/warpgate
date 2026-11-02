package main

import (
	"flag"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// mainArgsEnv makes the test binary act as wgcli with these
// (space-separated) arguments, so a test can observe flag parsing that exits.
const mainArgsEnv = "WGCLI_TEST_MAIN_ARGS"

func TestMain(m *testing.M) {
	if args, ok := os.LookupEnv(mainArgsEnv); ok {
		os.Args = append([]string{"wgcli"}, strings.Fields(args)...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// commandFlags returns the flag names "wgcli <cmd> -h" prints.
func commandFlags(t *testing.T, cmd string) []string {
	t.Helper()
	c := exec.Command(os.Args[0])
	c.Env = append(os.Environ(), mainArgsEnv+"="+cmd+" -h")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("wgcli %s -h: %v\n%s", cmd, err, out)
	}
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^  -(\S+)`).FindAllStringSubmatch(string(out), -1) {
		names = append(names, m[1])
	}
	if len(names) == 0 {
		t.Fatalf("wgcli %s -h printed no flags:\n%s", cmd, out)
	}
	return names
}

// usageCommands lists the commands in usageText, expanding "clip a|b" into
// "clip a", "clip b".
func usageCommands() []string {
	var cmds []string
	for _, m := range regexp.MustCompile(`(?m)^  ([a-z-]+)\s+(\S+)`).FindAllStringSubmatch(strings.SplitN(usageText, "\nCommands:\n", 2)[1], -1) {
		if m[1] == "guide" {
			continue
		}
		if !strings.Contains(m[2], "|") {
			cmds = append(cmds, m[1])
			continue
		}
		for _, sub := range strings.Split(m[2], "|") {
			cmds = append(cmds, m[1]+" "+sub)
		}
	}
	return cmds
}

// guideSections maps each "### wgcli <command> …" heading's command to its
// section text.
func guideSections() map[string]string {
	secs := map[string]string{}
	for _, part := range strings.Split(guide, "\n### wgcli ")[1:] {
		var words []string
		for _, w := range strings.Fields(strings.SplitN(part, "\n", 2)[0]) {
			if strings.ContainsAny(w[:1], "-[<") {
				break
			}
			words = append(words, w)
		}
		secs[strings.Join(words, " ")] = strings.Split(part, "\n## ")[0]
	}
	return secs
}

func mentionsFlag(text, name string) bool {
	return regexp.MustCompile(`(^|[^\w-])-` + regexp.QuoteMeta(name) + `([^\w-]|$)`).MatchString(text)
}

func TestGuideMatchesFlags(t *testing.T) {
	var global []string
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	addGlobalFlags(fs)
	fs.VisitAll(func(f *flag.Flag) { global = append(global, f.Name) })

	setup := strings.Split(strings.SplitN(guide, "\n## Setup", 2)[1], "\n## ")[0]
	for _, name := range global {
		if !mentionsFlag(setup, name) {
			t.Errorf("global flag -%s missing from the guide's Setup section", name)
		}
	}

	secs := guideSections()
	cmds := usageCommands()
	for name := range secs {
		if !slices.Contains(cmds, name) {
			t.Errorf("guide documents %q, which the usage text doesn't list", name)
		}
	}
	backticked := regexp.MustCompile("`-([a-z][a-z0-9-]*)")
	for _, cmd := range cmds {
		sec, ok := secs[cmd]
		if !ok {
			t.Errorf("command %q has no section in the guide", cmd)
			continue
		}
		flags := commandFlags(t, cmd)
		for _, name := range flags {
			if !slices.Contains(global, name) && !mentionsFlag(sec, name) {
				t.Errorf("guide section %q doesn't mention its flag -%s", cmd, name)
			}
		}
		for _, m := range backticked.FindAllStringSubmatch(sec, -1) {
			if !slices.Contains(flags, m[1]) {
				t.Errorf("guide section %q mentions -%s, which %q doesn't have", cmd, m[1], cmd)
			}
		}
	}
}
