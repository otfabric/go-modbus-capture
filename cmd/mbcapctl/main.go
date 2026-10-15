// SPDX-License-Identifier: MIT

// Command mbcapctl works with Modbus capture files (.mbcap, .mbcap.jsonl,
// .mbcap.csv): it inspects, verifies, dumps, converts, combines, splits,
// filters, trims and repairs them. It never opens a Modbus connection;
// recording and replaying are done by other tools, such as modbusctl.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Set by the linker (see Makefile).
var (
	version   = "0.0.0-dev"
	tag       = ""
	commit    = "unknown"
	buildDate = "unknown"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

type command struct {
	name    string
	args    string
	summary string
	run     func(e *env, fs *flag.FlagSet, args []string) error
	flags   func(o *options, fs *flag.FlagSet)
}

func commands() []*command {
	return []*command{
		{name: "info", args: "FILE", summary: "Show targets, streams, spans, sample counts, function mix and cadence", run: runInfo, flags: infoFlags},
		{name: "stat", args: "FILE", summary: "Same as info", run: runInfo, flags: infoFlags},
		{name: "verify", args: "FILE...", summary: "Check CRCs, structure and model invariants", run: runVerify},
		{name: "dump", args: "FILE", summary: "List every record in human-readable form", run: runDump, flags: dumpFlags},
		{name: "convert", args: "IN OUT", summary: "Convert between mbcap, jsonl and csv (lossless), or write a wide-csv view", run: runConvert, flags: outputFlags},
		{name: "cat", args: "-o OUT IN...", summary: "Concatenate captures, rebased on their wall-clock starts", run: runCat, flags: mergeFlags},
		{name: "merge", args: "-o OUT IN...", summary: "Merge captures, samples interleaved by wall-clock time", run: runMerge, flags: mergeFlags},
		{name: "split", args: "--by target|stream|fc IN", summary: "Write one capture per target, stream or function code", run: runSplit, flags: splitFlags},
		{name: "filter", args: "IN OUT", summary: "Keep a time window, targets, streams and/or function codes", run: runFilter, flags: filterFlags},
		{name: "trim", args: "IN OUT", summary: "Cut to a time window and rebase it to start at 0", run: runTrim, flags: trimFlags},
		{name: "repair", args: "IN OUT", summary: "Rewrite a truncated or damaged mbcap file with a new index", run: runRepair, flags: outputFlags},
		{name: "version", summary: "Print the version", run: runVersion},
	}
}

// options are the flag values of one command line. They live in the env of one
// run, not in package variables, so that run can be called concurrently.
type options struct {
	out      outputOptions
	infoJSON bool
	dumpWall bool
	mergeOut string
	splitBy  string
	splitDir string
	filter   struct {
		from, to                    timeFlag
		targets, streams, functions listFlag
	}
	trim struct{ from, to timeFlag }
}

// env is what a command reads and writes besides files.
type env struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	// step is the provenance step of the running command.
	step string
	// opt holds the flags of the running command.
	opt *options
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// errUsage marks an error in the command line.
var errUsage = errors.New("usage")

func usageErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errUsage, fmt.Sprintf(format, args...))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	e := &env{stdin: stdin, stdout: stdout, stderr: stderr, opt: &options{}}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(stdout)
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	var cmd *command
	for _, c := range commands() {
		if c.name == args[0] {
			cmd = c
		}
	}
	if cmd == nil {
		_, _ = fmt.Fprintf(stderr, "mbcapctl: unknown command %q\n\n", args[0])
		usage(stderr)
		return exitUsage
	}
	fs := flag.NewFlagSet("mbcapctl "+cmd.name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: mbcapctl %s [flags] %s\n\n%s.\n", cmd.name, cmd.args, cmd.summary)
		if hasFlags(fs) {
			_, _ = fmt.Fprintln(stderr, "\nFlags:")
			fs.PrintDefaults()
		}
	}
	if cmd.flags != nil {
		cmd.flags(e.opt, fs)
	}
	if err := fs.Parse(interleaved(fs, args[1:])); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	e.step = provenanceStep(cmd.name, fs)
	err := cmd.run(e, fs, fs.Args())
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintf(stderr, "mbcapctl %s: %v\n", cmd.name, strings.TrimPrefix(err.Error(), errUsage.Error()+": "))
		fs.Usage()
		return exitUsage
	default:
		_, _ = fmt.Fprintf(stderr, "mbcapctl %s: %v\n", cmd.name, err)
		return exitError
	}
}

// interleaved moves flags after positional arguments to the front, so that
// "mbcapctl filter in.mbcap out.mbcap --fc 3" works as well as flags first.
func interleaved(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil && !isBool(f) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, pos...)
}

func isBool(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

func hasFlags(fs *flag.FlagSet) bool {
	n := 0
	fs.VisitAll(func(*flag.Flag) { n++ })
	return n > 0
}

// provenanceStep describes the command and its flags, without file or
// directory names, for the provenance tag of the files it writes.
func provenanceStep(name string, fs *flag.FlagSet) string {
	var parts []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "o" || f.Name == "output" || f.Name == "dir" {
			return
		}
		parts = append(parts, fmt.Sprintf("--%s=%s", f.Name, f.Value.String()))
	})
	sort.Strings(parts)
	step := "mbcapctl " + version + " " + name
	if len(parts) > 0 {
		step += " " + strings.Join(parts, " ")
	}
	return step
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintf(w, `mbcapctl works with Modbus capture files (.mbcap, .mbcap.jsonl, .mbcap.csv).
It never opens a Modbus connection.

Usage: mbcapctl COMMAND [flags] ARGS

Commands:
`)
	for _, c := range commands() {
		_, _ = fmt.Fprintf(w, "  %-8s %s\n", c.name, c.summary)
	}
	_, _ = fmt.Fprintf(w, `
Files are read in any encoding (detected from their content); "-" is standard
input or output. The output encoding follows the file extension, or --format.
Run "mbcapctl COMMAND -h" for the flags of a command.
`)
}

func runVersion(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) != 0 {
		return usageErr("version takes no arguments")
	}
	_, _ = fmt.Fprintf(e.stdout, "mbcapctl %s (tag %s, commit %s, built %s)\n", version, orNone(tag), commit, buildDate)
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
