// SPDX-License-Identifier: MIT

package main

// Shell completion without a CLI library (REQUIREMENTS.md decision 16): the
// scripts are generated from the command table and the flag sets the
// commands register, so a new command or flag completes without changes
// here. Flags with a fixed set of values use choiceVar, file and directory
// flags pathVar and dirVar; every other non-boolean flag takes a free value.

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

var shells = []string{"bash", "zsh", "fish", "powershell"}

// captureExts are the extensions of capture files, in every encoding
// (.mbcap.jsonl and .mbcap.csv end in .jsonl and .csv).
var captureExts = []string{"mbcap", "jsonl", "csv"}

// choiceFlag is a string flag with a fixed set of values. It stores what it
// is given: the command checks the value, as before.
type choiceFlag struct {
	p       *string
	choices []string
}

func (c *choiceFlag) String() string {
	if c.p == nil {
		return ""
	}
	return *c.p
}

func (c *choiceFlag) Set(s string) error { *c.p = s; return nil }

func choiceVar(fs *flag.FlagSet, p *string, name, value, usage string, choices ...string) {
	*p = value
	fs.Var(&choiceFlag{p: p, choices: choices}, name, usage)
}

// pathFlag is a string flag naming a file with one of exts, or a directory.
type pathFlag struct {
	choiceFlag
	exts []string
	dir  bool
}

func pathVar(fs *flag.FlagSet, p *string, name, value, usage string, exts ...string) {
	*p = value
	fs.Var(&pathFlag{choiceFlag: choiceFlag{p: p}, exts: exts}, name, usage)
}

func dirVar(fs *flag.FlagSet, p *string, name, value, usage string) {
	*p = value
	fs.Var(&pathFlag{choiceFlag: choiceFlag{p: p}, dir: true}, name, usage)
}

// What a flag or a positional argument completes.
type compKind int

const (
	compNone   compKind = iota // nothing (a boolean flag, or no arguments)
	compValue                  // a free value: no completion
	compChoice                 // one of values
	compFiles                  // files with one of values as extension, and directories
	compDir                    // directories
)

type compSpec struct {
	kind   compKind
	values []string
}

type compFlag struct {
	name  string
	usage string
	compSpec
}

// option is the flag as typed: Go's flag package takes one or two dashes;
// the scripts offer -o for one-letter flags and --name for the others.
func (f compFlag) option() string {
	if len(f.name) == 1 {
		return "-" + f.name
	}
	return "--" + f.name
}

// alias is the other spelling of the flag, which completes values too.
func (f compFlag) alias() string {
	if len(f.name) == 1 {
		return "--" + f.name
	}
	return "-" + f.name
}

type compCmd struct {
	name    string
	summary string
	flags   []compFlag
	args    compSpec
}

// completionModel reads the command table and the flags each command
// registers.
func completionModel() []compCmd {
	var out []compCmd
	for _, c := range commands() {
		fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
		if c.flags != nil {
			c.flags(fs)
		}
		cc := compCmd{name: c.name, summary: c.summary}
		fs.VisitAll(func(f *flag.Flag) {
			cc.flags = append(cc.flags, compFlag{name: f.Name, usage: f.Usage, compSpec: flagSpec(f)})
		})
		switch {
		case c.values != nil:
			cc.args = compSpec{kind: compChoice, values: c.values}
		case strings.Contains(c.args, "FILE") || strings.Contains(c.args, "IN") || strings.Contains(c.args, "OUT"):
			cc.args = compSpec{kind: compFiles, values: captureExts}
		}
		out = append(out, cc)
	}
	return out
}

func flagSpec(f *flag.Flag) compSpec {
	switch v := f.Value.(type) {
	case *pathFlag:
		if v.dir {
			return compSpec{kind: compDir}
		}
		return compSpec{kind: compFiles, values: v.exts}
	case *choiceFlag:
		return compSpec{kind: compChoice, values: v.choices}
	}
	if isBool(f) {
		return compSpec{kind: compNone}
	}
	return compSpec{kind: compValue}
}

func runCompletion(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) != 1 {
		return usageErr("completion takes one shell: %s", strings.Join(shells, ", "))
	}
	gen := map[string]func(io.Writer, []compCmd){
		"bash": bashCompletion, "zsh": zshCompletion, "fish": fishCompletion, "powershell": powershellCompletion,
	}[args[0]]
	if gen == nil {
		return usageErr("unknown shell %q (want %s)", args[0], strings.Join(shells, ", "))
	}
	gen(e.stdout, completionModel())
	return nil
}

func commandNames(cmds []compCmd) []string {
	names := make([]string, 0, len(cmds)+1)
	for _, c := range cmds {
		names = append(names, c.name)
	}
	return append(names, "help")
}

func flagOptions(c compCmd) []string {
	opts := make([]string, 0, len(c.flags)+1)
	for _, f := range c.flags {
		opts = append(opts, f.option())
	}
	opts = append(opts, "--help")
	sort.Strings(opts)
	return opts
}

// sq quotes s for a POSIX shell, zsh or PowerShell single-quoted string.
func sq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func psq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// fishq quotes s for a fish single-quoted string.
func fishq(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func bashCompletion(w io.Writer, cmds []compCmd) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p(`# bash completion for mbcapctl; generated by "mbcapctl completion bash".

_mbcapctl_files() {
    local f e
    compopt -o filenames 2>/dev/null
    while IFS= read -r f; do
        if [ -d "$f" ]; then
            COMPREPLY+=("$f")
            continue
        fi
        for e in "$@"; do
            case "$f" in *."$e") COMPREPLY+=("$f"); break ;; esac
        done
    done < <(compgen -f -- "$cur")
}

_mbcapctl() {
    local cur prev
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    # --flag=value: bash splits at "=".
    if [ "$cur" = "=" ]; then
        cur=""
    elif [ "$prev" = "=" ] && [ "$COMP_CWORD" -ge 3 ]; then
        prev="${COMP_WORDS[COMP_CWORD-2]}"
    fi
    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=($(compgen -W %s -- "$cur"))
        return
    fi
    case "${COMP_WORDS[1]}" in
`, sq(strings.Join(commandNames(cmds), " ")))
	bashSpec := func(indent string, s compSpec) {
		switch s.kind {
		case compChoice:
			p("%sCOMPREPLY=($(compgen -W %s -- \"$cur\"))\n", indent, sq(strings.Join(s.values, " ")))
		case compFiles:
			p("%s_mbcapctl_files %s\n", indent, strings.Join(s.values, " "))
		case compDir:
			p("%scompopt -o filenames 2>/dev/null\n%sCOMPREPLY=($(compgen -d -- \"$cur\"))\n", indent, indent)
		default:
			p("%s:\n", indent)
		}
	}
	for _, c := range cmds {
		p("    %s)\n", c.name)
		var valued []compFlag
		for _, f := range c.flags {
			if f.kind != compNone {
				valued = append(valued, f)
			}
		}
		if len(valued) > 0 {
			p("        case \"$prev\" in\n")
			for _, f := range valued {
				p("        %s|%s)\n", f.option(), f.alias())
				bashSpec("            ", f.compSpec)
				p("            return ;;\n")
			}
			p("        esac\n")
		}
		p("        if [[ \"$cur\" == -* ]]; then\n")
		p("            COMPREPLY=($(compgen -W %s -- \"$cur\"))\n", sq(strings.Join(flagOptions(c), " ")))
		p("            return\n        fi\n")
		bashSpec("        ", c.args)
		p("        ;;\n")
	}
	p(`    esac
}

complete -F _mbcapctl mbcapctl
`)
}

// zdesc escapes a description for an _arguments or _describe spec.
func zdesc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`, `:`, `\:`).Replace(s)
}

func zshAction(s compSpec) string {
	switch s.kind {
	case compChoice:
		return "(" + strings.Join(s.values, " ") + ")"
	case compFiles:
		return `_files -g "*.(` + strings.Join(s.values, "|") + `)"`
	case compDir:
		return "_files -/"
	default:
		return " "
	}
}

func zshCompletion(w io.Writer, cmds []compCmd) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p(`#compdef mbcapctl
# zsh completion for mbcapctl; generated by "mbcapctl completion zsh".

_mbcapctl() {
    local -a commands
    commands=(
`)
	for _, c := range cmds {
		p("        %s\n", sq(c.name+":"+zdesc(c.summary)))
	}
	p(`        'help:Show the commands'
    )
    if (( CURRENT == 2 )); then
        _describe -t commands 'mbcapctl command' commands
        return
    fi
    local cmd=$words[2]
    shift words
    (( CURRENT-- ))
    case $cmd in
`)
	for _, c := range cmds {
		p("    %s)\n        _arguments \\\n", c.name)
		p("            '(- *)'{-h,--help}'[show the flags of %s]' \\\n", c.name)
		for _, f := range c.flags {
			spec := "[" + zdesc(f.usage) + "]"
			if f.kind != compNone {
				spec += ":" + f.name + ":" + zshAction(f.compSpec)
			}
			p("            '(%s %s)'{%s,%s}%s \\\n", f.option(), f.alias(), f.option(), f.alias(), sq(spec))
		}
		if c.args.kind == compNone {
			p("            && return 0\n        ;;\n")
			continue
		}
		p("            %s && return 0\n        ;;\n", sq("*:argument:"+zshAction(c.args)))
	}
	p(`    esac
}

if [ "$funcstack[1]" = "_mbcapctl" ]; then
    _mbcapctl "$@"
else
    compdef _mbcapctl mbcapctl
fi
`)
}

func fishCompletion(w io.Writer, cmds []compCmd) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("# fish completion for mbcapctl; generated by \"mbcapctl completion fish\".\n\n")
	p("complete -c mbcapctl -f\n")
	names := commandNames(cmds)
	p("complete -c mbcapctl -n %s -a help -d 'Show the commands'\n", fishq("not __fish_seen_subcommand_from "+strings.Join(names, " ")))
	fishFiles := func(exts []string) string {
		var calls []string
		for _, e := range exts {
			calls = append(calls, "__fish_complete_suffix ."+e)
		}
		return "(" + strings.Join(calls, "; ") + ")"
	}
	fishSpec := func(s compSpec) string {
		switch s.kind {
		case compChoice:
			return " -x -a " + fishq(strings.Join(s.values, " "))
		case compFiles:
			return " -r -k -a " + fishq(fishFiles(s.values))
		case compDir:
			return " -x -a '(__fish_complete_directories)'"
		case compValue:
			return " -x"
		default:
			return ""
		}
	}
	for _, c := range cmds {
		p("\ncomplete -c mbcapctl -n %s -a %s -d %s\n", fishq("not __fish_seen_subcommand_from "+strings.Join(names, " ")), c.name, fishq(c.summary))
		cond := fishq("__fish_seen_subcommand_from " + c.name)
		for _, f := range c.flags {
			opt := "-l " + f.name
			if len(f.name) == 1 {
				opt = "-s " + f.name
			}
			p("complete -c mbcapctl -n %s %s -d %s%s\n", cond, opt, fishq(f.usage), fishSpec(f.compSpec))
		}
		switch c.args.kind {
		case compChoice:
			p("complete -c mbcapctl -n %s -a %s\n", cond, fishq(strings.Join(c.args.values, " ")))
		case compFiles:
			p("complete -c mbcapctl -n %s -k -a %s\n", cond, fishq(fishFiles(c.args.values)))
		default:
		}
	}
}

func psSpec(s compSpec) string {
	kind := map[compKind]string{compNone: "none", compValue: "value", compChoice: "choice", compFiles: "files", compDir: "dir"}[s.kind]
	vals := make([]string, len(s.values))
	for i, v := range s.values {
		if s.kind == compFiles {
			v = "." + v
		}
		vals[i] = psq(v)
	}
	return fmt.Sprintf("@{ Kind = %s; Values = @(%s) }", psq(kind), strings.Join(vals, ", "))
}

func powershellCompletion(w io.Writer, cmds []compCmd) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	p("# PowerShell completion for mbcapctl; generated by \"mbcapctl completion powershell\".\n\n")
	p("$mbcapctlCommands = [ordered]@{\n")
	for _, c := range cmds {
		p("    %s = @{\n        Summary = %s\n        Args = %s\n        Flags = [ordered]@{\n", psq(c.name), psq(c.summary), psSpec(c.args))
		p("            '--help' = @{ Kind = 'none'; Values = @() }\n")
		for _, f := range c.flags {
			p("            %s = %s\n", psq(f.option()), psSpec(f.compSpec))
			p("            %s = %s\n", psq(f.alias()), psSpec(f.compSpec))
		}
		p("        }\n    }\n")
	}
	p("    'help' = @{ Summary = 'Show the commands'; Args = @{ Kind = 'none'; Values = @() }; Flags = [ordered]@{} }\n}\n")
	p(`
Register-ArgumentCompleter -Native -CommandName 'mbcapctl', 'mbcapctl.exe' -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $words = @($commandAst.CommandElements |
        Where-Object { $_.Extent.EndOffset -le $cursorPosition } |
        ForEach-Object { $_.ToString() })
    if ($wordToComplete -ne '' -and $words.Count -gt 0) {
        if ($words.Count -gt 1) { $words = $words[0..($words.Count - 2)] } else { $words = @() }
    }
    $result = {
        param($value, $tip)
        [System.Management.Automation.CompletionResult]::new($value, $value, 'ParameterValue', $tip)
    }
    if ($words.Count -le 1) {
        foreach ($name in $mbcapctlCommands.Keys) {
            if ($name -like "$wordToComplete*") { & $result $name $mbcapctlCommands[$name].Summary }
        }
        return
    }
    $cmd = $mbcapctlCommands[$words[1]]
    if ($null -eq $cmd) { return }
    $spec = $cmd.Args
    $prev = $words[$words.Count - 1]
    if ($words.Count -gt 2 -and $cmd.Flags.Contains($prev) -and $cmd.Flags[$prev].Kind -ne 'none') {
        $spec = $cmd.Flags[$prev]
    } elseif ($wordToComplete.StartsWith('-')) {
        foreach ($flag in $cmd.Flags.Keys) {
            if ($flag.StartsWith('--') -or $flag.Length -eq 2) {
                if ($flag -like "$wordToComplete*") { & $result $flag $flag }
            }
        }
        return
    }
    switch ($spec.Kind) {
        'choice' {
            foreach ($v in $spec.Values) { if ($v -like "$wordToComplete*") { & $result $v $v } }
        }
        { $_ -eq 'files' -or $_ -eq 'dir' } {
            $dir = Split-Path -Path $wordToComplete -Parent
            Get-ChildItem -Path "$wordToComplete*" -ErrorAction SilentlyContinue |
                Where-Object { $_.PSIsContainer -or ($spec.Kind -eq 'files' -and $spec.Values -contains $_.Extension) } |
                ForEach-Object {
                    $path = if ($dir) { Join-Path $dir $_.Name } else { $_.Name }
                    [System.Management.Automation.CompletionResult]::new($path, $path, 'ProviderItem', $path)
                }
        }
    }
}.GetNewClosure()
`)
}
