// Package cli implements cppm's command line.
//
// Deliberately stdlib-only. cppm's whole thesis is that C++ dependency handling
// is painful, so the tool itself builds with `go build` and nothing else; the
// subcommand surface is small and regular enough not to need a framework.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/cmakegen"
)

// Version is cppm's own version, stamped into generated files and the lockfile.
//
// This is the fallback for a build from source; a release binary gets the exact
// tag via -ldflags. The "-dev" suffix is what distinguishes the two, so it must
// stay: a lockfile's generated_by is the only record of what produced it.
var Version = "0.3.2-dev"

// Command is one subcommand.
//
// flagSet builds a fresh flag set each time it is called, so `cppm help <cmd>`
// can list a command's flags without running it and without a shared mutable
// parser.
type Command struct {
	Name    string
	Aliases []string
	Summary string
	Usage   string
	Long    string
	flagSet func() *flag.FlagSet
	Run     func(args []string) error
}

var commands []*Command

func register(c *Command) { commands = append(commands, c) }

func init() {
	register(cmdInit())
	register(cmdAdd())
	register(cmdInstall())
	register(cmdRemove())
	register(cmdUpdate())
	register(cmdSync())
	register(cmdSources())
	register(cmdBuild())
	register(cmdTest())
	register(cmdList())
	register(cmdInfo())
	register(cmdDoctor())
	register(cmdClean())
}

func lookup(name string) *Command {
	for _, c := range commands {
		if c.Name == name {
			return c
		}
		for _, a := range c.Aliases {
			if a == name {
				return c
			}
		}
	}
	return nil
}

// allInvocableNames includes aliases, so typo suggestions cover them too.
func allInvocableNames() []string {
	var out []string
	for _, c := range commands {
		out = append(out, c.Name)
		out = append(out, c.Aliases...)
	}
	return out
}

// Main runs the CLI and returns a process exit code.
func Main(args []string) int {
	cmakegen.Version = Version

	if len(args) == 0 {
		usage(os.Stdout)
		return 0
	}

	switch args[0] {
	case "-h", "--help", "help":
		if len(args) > 1 {
			c := lookup(args[1])
			if c == nil {
				fmt.Fprintf(os.Stderr, "cppm: unknown command %q\n", args[1])
				return 2
			}
			commandHelp(os.Stdout, c)
			return 0
		}
		usage(os.Stdout)
		return 0
	case "-V", "--version", "version":
		fmt.Printf("cppm %s\n", Version)
		return 0
	}

	if strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(os.Stderr, "cppm: unknown flag %q\n\n", args[0])
		usage(os.Stderr)
		return 2
	}

	c := lookup(args[0])
	if c == nil {
		fmt.Fprintf(os.Stderr, "cppm: unknown command %q\n", args[0])
		if s := suggest(args[0]); s != "" {
			fmt.Fprintf(os.Stderr, "did you mean %q?\n", s)
		}
		fmt.Fprintln(os.Stderr, "\nrun 'cppm help' for the list of commands")
		return 2
	}

	switch err := c.Run(args[1:]); {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		commandHelp(os.Stdout, c)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "cppm %s: %v\n", c.Name, err)
		return 1
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `cppm %s -- start a C++ project, add dependencies, build it.

usage:
    cppm <command> [arguments]

commands:
`, Version)
	width := 0
	for _, c := range commands {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	for _, c := range commands {
		fmt.Fprintf(w, "    %-*s  %s\n", width, c.Name, c.Summary)
	}
	var aliasPairs []string
	for _, c := range commands {
		if len(c.Aliases) > 0 {
			aliasPairs = append(aliasPairs, fmt.Sprintf("%s = %s", strings.Join(c.Aliases, ", "), c.Name))
		}
	}
	if len(aliasPairs) > 0 {
		fmt.Fprintf(w, "\naliases: %s\n", strings.Join(aliasPairs, "; "))
	}
	fmt.Fprint(w, `
run 'cppm help <command>' for details on one command.

getting started:
    cppm init MyApp
    cd MyApp
    cppm build

in a project you just cloned:
    cppm install
`)
}

func commandHelp(w io.Writer, c *Command) {
	fmt.Fprintf(w, "cppm %s -- %s\n\nusage:\n    %s\n", c.Name, c.Summary, c.Usage)
	if len(c.Aliases) > 0 {
		fmt.Fprintf(w, "\naliases: %s\n", strings.Join(c.Aliases, ", "))
	}
	if c.Long != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimRight(c.Long, "\n"))
	}
	if c.flagSet != nil {
		fs := c.flagSet()
		var b strings.Builder
		fs.SetOutput(&b)
		fs.PrintDefaults()
		if strings.TrimSpace(b.String()) != "" {
			fmt.Fprintf(w, "\nflags:\n%s", b.String())
		}
	}
}

// newFlagSet returns a flag set that prints nothing on error, so the CLI
// controls all user-facing output.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// suggest returns the closest command name, for a typo hint.
func suggest(given string) string {
	best, bestDist := "", 1<<30
	for _, n := range allInvocableNames() {
		if d := editDistance(given, n); d < bestDist {
			best, bestDist = n, d
		}
	}
	if bestDist <= 2 {
		return best
	}
	return ""
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		copy(prev, cur)
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// CommandNames returns the registered command names in sorted order.
func CommandNames() []string {
	out := make([]string, 0, len(commands))
	for _, c := range commands {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}
