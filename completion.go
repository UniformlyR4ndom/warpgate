package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"
)

type flagValueKind int

const (
	valueText flagValueKind = iota // no completion
	valueDir
	valueFile
	valueShell
)

// flagValueKinds must list every non-boolean flag.
var flagValueKinds = map[string]flagValueKind{
	"root":            valueDir,
	"ul-root":         valueDir,
	"db":              valueFile,
	"cert":            valueFile,
	"cert-key":        valueFile,
	"client-root":     valueFile,
	"listen":          valueText,
	"client-fp":       valueText,
	"auth-key":        valueText,
	"ws-frame-max":    valueText,
	"path-prefix":     valueText,
	"ip-allow":        valueText,
	"ip-reg-path":     valueText,
	"ip-reg-max":      valueText,
	"ip-reg-window":   valueText,
	"public-url":      valueText,
	"trusted-proxies": valueText,
	"completion":      valueShell,
}

var completionShells = []string{"bash", "zsh"}

type completionFlag struct {
	name, desc, valueName string
	isBool                bool
	kind                  flagValueKind
}

func completionFlags(fs *flag.FlagSet) ([]completionFlag, error) {
	var flags []completionFlag
	var err error
	fs.VisitAll(func(f *flag.Flag) {
		valueName, usage := flag.UnquoteUsage(f)
		desc, _, _ := strings.Cut(usage, "; ")
		cf := completionFlag{name: f.Name, desc: desc, valueName: valueName}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			cf.isBool = true
		} else if kind, ok := flagValueKinds[f.Name]; ok {
			cf.kind = kind
		} else if err == nil {
			err = fmt.Errorf("flag -%s has no completion value kind", f.Name)
		}
		flags = append(flags, cf)
	})
	sort.Slice(flags, func(i, j int) bool { return flags[i].name < flags[j].name })
	return flags, err
}

func completionScript(fs *flag.FlagSet, shell string) (string, error) {
	flags, err := completionFlags(fs)
	if err != nil {
		return "", err
	}
	switch shell {
	case "bash":
		return bashCompletion(flags), nil
	case "zsh":
		return zshCompletion(flags), nil
	}
	return "", fmt.Errorf("unsupported shell %q for -completion (want %s)", shell, strings.Join(completionShells, " or "))
}

// bashFlagPattern matches a flag with one or two dashes, as package flag accepts both.
func bashFlagPattern(names []string) string {
	pats := make([]string, 0, 2*len(names))
	for _, n := range names {
		pats = append(pats, "-"+n, "--"+n)
	}
	return strings.Join(pats, "|")
}

func bashCompletion(flags []completionFlag) string {
	byKind := map[flagValueKind][]string{}
	var all []string
	for _, f := range flags {
		all = append(all, "-"+f.name)
		if !f.isBool {
			byKind[f.kind] = append(byKind[f.kind], f.name)
		}
	}
	var b strings.Builder
	b.WriteString("# bash completion for warpgate\n_warpgate() {\n")
	b.WriteString("    local cur=${COMP_WORDS[COMP_CWORD]} prev=${COMP_WORDS[COMP_CWORD-1]}\n")
	b.WriteString("    COMPREPLY=()\n    case $prev in\n")
	fmt.Fprintf(&b, "        %s)\n            compopt -o filenames\n            mapfile -t COMPREPLY < <(compgen -d -- \"$cur\")\n            return ;;\n",
		bashFlagPattern(byKind[valueDir]))
	fmt.Fprintf(&b, "        %s)\n            compopt -o filenames\n            mapfile -t COMPREPLY < <(compgen -f -- \"$cur\")\n            return ;;\n",
		bashFlagPattern(byKind[valueFile]))
	fmt.Fprintf(&b, "        %s)\n            mapfile -t COMPREPLY < <(compgen -W %q -- \"$cur\")\n            return ;;\n",
		bashFlagPattern(byKind[valueShell]), strings.Join(completionShells, " "))
	fmt.Fprintf(&b, "        %s)\n            return ;;\n", bashFlagPattern(byKind[valueText]))
	b.WriteString("    esac\n")
	fmt.Fprintf(&b, "    mapfile -t COMPREPLY < <(compgen -W '%s' -- \"$cur\")\n}\n", strings.Join(all, " "))
	b.WriteString("complete -F _warpgate warpgate\n")
	return b.String()
}

// zshQuote escapes s for a single-quoted _arguments spec; escapeColon also
// escapes the field separator, which descriptions in brackets don't need.
func zshQuote(s string, escapeColon bool) string {
	r := []string{`'`, `'\''`, `[`, `\[`, `]`, `\]`}
	if escapeColon {
		r = append(r, `:`, `\:`)
	}
	return strings.NewReplacer(r...).Replace(s)
}

func zshCompletion(flags []completionFlag) string {
	var b strings.Builder
	b.WriteString("#compdef warpgate\n# zsh completion for warpgate\n_warpgate() {\n    _arguments \\\n")
	for _, f := range flags {
		fmt.Fprintf(&b, "        '-%s[%s]", f.name, zshQuote(f.desc, false))
		if !f.isBool {
			switch f.kind {
			case valueDir:
				b.WriteString(":directory:_files -/")
			case valueFile:
				b.WriteString(":file:_files")
			case valueShell:
				fmt.Fprintf(&b, ":shell:(%s)", strings.Join(completionShells, " "))
			default:
				fmt.Fprintf(&b, ":%s: ", zshQuote(f.valueName, true))
			}
		}
		b.WriteString("' \\\n")
	}
	b.WriteString("\n}\n")
	b.WriteString("if [ \"$funcstack[1]\" = _warpgate ]; then\n    _warpgate \"$@\"\nelse\n    compdef _warpgate warpgate\nfi\n")
	return b.String()
}
