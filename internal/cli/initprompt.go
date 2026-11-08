package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/manifest"
	"github.com/mehdi-shokohi/cppm/internal/toolchain"
)

// prompter asks a series of questions on one stream.
//
// The bufio.Reader is held for the whole run rather than created per question:
// bufio reads ahead, so a fresh reader each time would throw away anything the
// user had already typed past the current line. (readLine in add.go gets away
// with a throwaway reader because it asks exactly once per command.)
//
// eof latches. Once the input is closed -- a ^D, or a stdin that was never a
// terminal in the first place -- every remaining question takes its default
// instead of spinning on an EOF that will never turn into an answer.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
	eof bool
}

func newPrompter(r io.Reader, w io.Writer) *prompter {
	return &prompter{in: bufio.NewReader(r), out: w}
}

func (p *prompter) printf(format string, a ...any) {
	fmt.Fprintf(p.out, format, a...)
}

// ask prints "question [def]: " and returns the answer, or def for a bare Enter.
func (p *prompter) ask(question, def string) string {
	return p.read(question+" ["+def+"]", def)
}

// read prints "label: " and returns the answer, or def for a bare Enter. It is
// separate from ask so yesNo can show "[Y/n]" in place of the literal default
// without printing both.
func (p *prompter) read(label, def string) string {
	if p.eof {
		return def
	}
	p.printf("%s: ", label)
	line, err := p.in.ReadString('\n')
	if err != nil {
		p.eof = true
		if strings.TrimSpace(line) == "" {
			p.printf("\n")
			return def
		}
	}
	if answer := strings.TrimSpace(line); answer != "" {
		return answer
	}
	return def
}

func (p *prompter) yesNo(question string, def bool) bool {
	hint := "y/N"
	defStr := "n"
	if def {
		hint = "Y/n"
		defStr = "y"
	}
	for {
		switch strings.ToLower(p.read(question+" ["+hint+"]", defStr)) {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		default:
			p.printf("  please answer y or n.\n")
		}
	}
}

// initQuestions is what the caller already knows before asking anything.
type initQuestions struct {
	// dirArg is the directory as the user typed it, reproduced in the recap.
	dirArg        string
	suggestedName string
	// nameChanged reports that suggestedName is not the directory name, so the
	// prompt can say why it is offering something else.
	nameChanged bool
	// compilers holds the candidates, the one cppm would pick by itself first.
	// Empty when no compiler was found, in which case the question is skipped:
	// an answer cppm cannot verify is worse than letting CMake choose.
	compilers []string
}

// askInit fills in o from the user's answers and reports whether to go ahead.
//
// Every question's default is the value the flags would have produced, so
// pressing Enter through the whole thing creates exactly what a bare
// `cppm init` always created -- with one deliberate exception, the test
// question, which defaults to yes for a library. See there for why.
func askInit(r io.Reader, w io.Writer, q initQuestions, o *initOpts) bool {
	p := newPrompter(r, w)

	p.printf("a few questions; Enter takes the default in brackets.\n")
	p.printf("pass any flag (or --non-interactive) to skip them entirely.\n\n")

	if q.nameChanged {
		p.printf("the directory name is not usable as a CMake target name, so cppm\n")
		p.printf("suggests a cleaned-up one:\n")
	}
	name, changed := sanitizeName(p.ask("project name", q.suggestedName))
	if changed {
		p.printf("  note: using %q -- a target name takes letters, digits, '_', '.' and '-'\n", name)
	}
	o.name = name

	p.printf("\nwhat is it?\n")
	p.printf("   1) executable -- src/main.cpp, builds a program\n")
	p.printf("   2) library    -- src/%s.cpp and include/, for other projects to link\n", name)
	for {
		answer := strings.ToLower(p.ask("kind", "1"))
		if answer == "1" || answer == "exe" || answer == "executable" {
			o.lib = false
			break
		}
		if answer == "2" || answer == "lib" || answer == "library" {
			o.lib = true
			break
		}
		p.printf("  not one of the choices: 1 or 2.\n")
	}

	// The default differs by kind, and that is the point rather than an
	// oversight. A test target can link a library, so for --lib the scaffolded
	// test exercises the real code from the first commit. An executable cannot
	// be linked into another target, so the same scaffold in an executable
	// project is a test that can only check itself until the user splits a
	// library out -- offered, but not on the Enter key.
	p.printf("\n")
	if o.lib {
		p.printf("a tests/ directory and a first test, linked against %s.\n", name)
	} else {
		p.printf("a tests/ directory and a first test. An executable cannot be linked\n")
		p.printf("into a test, so it starts out checking only itself; move shared code\n")
		p.printf("into a library target when you want it under test.\n")
	}
	o.withTests = p.yesNo("scaffold tests", o.lib)

	p.printf("\n")
	for {
		answer := p.ask("C++ standard", strconv.Itoa(o.std))
		n, err := strconv.Atoi(answer)
		if err == nil && validStandard(n) {
			o.std = n
			break
		}
		p.printf("  %q is not one of %v.\n", answer, manifest.ValidStandards)
	}

	if len(q.compilers) > 0 {
		p.printf("\nthe compiler is written into cppm.json and the presets, because\n")
		p.printf("CMAKE_CXX_COMPILER has to be set before project() runs.\n")
		if len(q.compilers) == 1 {
			o.compiler = p.ask("compiler", q.compilers[0])
		} else {
			for i, c := range q.compilers {
				p.printf("  %2d) %s\n", i+1, c)
			}
			p.printf("  or type a name or a full path\n")
			answer := p.ask("compiler", "1")
			if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(q.compilers) {
				o.compiler = q.compilers[n-1]
			} else {
				o.compiler = answer
			}
		}
	}

	p.printf("\n  name       %s\n", o.name)
	p.printf("  kind       %s\n", kindWord(o.lib))
	if o.withTests {
		p.printf("  tests      yes -- tests/%s_test.cpp\n", name)
	} else {
		p.printf("  tests      no\n")
	}
	p.printf("  standard   C++%d\n", o.std)
	if o.compiler != "" {
		p.printf("  compiler   %s\n", o.compiler)
	}
	p.printf("\nthe same project without the questions:\n    %s\n\n", equivalentInit(q, o))

	return p.yesNo("create it", true)
}

// equivalentInit renders the flags that reproduce these answers.
//
// Printed so the prompt teaches its own way out: the next project, or the same
// one in a script or a README, does not want to be asked.
func equivalentInit(q initQuestions, o *initOpts) string {
	parts := []string{"cppm init"}
	if q.dirArg != "" && q.dirArg != "." {
		parts = append(parts, q.dirArg)
	}
	if o.name != q.suggestedName {
		parts = append(parts, "--name "+o.name)
	}
	if o.lib {
		parts = append(parts, "--lib")
	}
	if o.withTests {
		parts = append(parts, "--with-tests")
	}
	parts = append(parts, "--std "+strconv.Itoa(o.std))
	// Only when it differs from what detection would have found anyway: a
	// --compiler in a command line that gets copied to another machine is a
	// hard failure there if that compiler is missing.
	if len(q.compilers) > 0 && o.compiler != "" && o.compiler != q.compilers[0] {
		parts = append(parts, "--compiler "+o.compiler)
	}
	return strings.Join(parts, " ")
}

// detectCompilers lists the compilers worth offering, cppm's own pick first.
//
// toolchain.Compiler() honours $CXX, so the head of the list is the answer the
// non-interactive path would have given; the rest are alternatives that are
// actually installed. Nothing is offered that is not on PATH.
func detectCompilers() []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if t := toolchain.Compiler(); t.Found() {
		add(t.Name)
	}
	for _, name := range []string{"g++", "clang++"} {
		if !seen[name] && toolchain.Find(name).Found() {
			add(name)
		}
	}
	return out
}

// isTerminalSession reports whether cppm is talking to a human.
//
// Both ends are checked, and stdin alone would not do. A stdin redirected from
// /dev/null satisfies ModeCharDevice -- /dev/null is a character device -- so a
// CI job or a test harness running `cppm init` with no flags would be asked
// questions nobody can answer. Captured output is the reliable tell: a pipe is
// not a character device, so `cppm init Foo > log` and exec.Command's
// CombinedOutput both fall through to the defaults.
func isTerminalSession() bool {
	return isInteractive() && isCharDevice(os.Stdout)
}

func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
