package filename

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b", "tab\tname", strings.Repeat("n", 256)} {
		if Valid(name) {
			t.Errorf("Valid(%q) = true", name)
		}
	}
	for _, name := range []string{"a", "report.pdf", "with space", "ünïcödé", strings.Repeat("n", 255)} {
		if !Valid(name) {
			t.Errorf("Valid(%q) = false", name)
		}
	}
}

func TestValidWindows(t *testing.T) {
	for _, name := range []string{"a:b", "file.txt:ads", "c:", "q?", "s*", `x"y`, "<", ">", "p|q", "dot.", "space ",
		"CON", "con", "nul.txt", "Aux.tar.gz", "COM1", "lpt9.log", "NUL .txt", "CONIN$"} {
		if ValidWindows(name) {
			t.Errorf("ValidWindows(%q) = true", name)
		}
	}
	for _, name := range []string{"a", "CONSOLE", "nul_", "COM", "COM10", "LPTX", ".hidden", "a.b.c"} {
		if !ValidWindows(name) {
			t.Errorf("ValidWindows(%q) = false", name)
		}
	}
}
