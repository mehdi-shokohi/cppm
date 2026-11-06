// Package toolchain locates the external programs cppm drives and runs them.
//
// cppm never owns the build: it shells out to cmake, ninja and git. All
// execution goes through Run, which uses exec.Command with an argv slice and so
// never involves a shell. That satisfies the "never concatenate user input into
// a shell command" requirement by construction.
package toolchain

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Tool is a located external program.
type Tool struct {
	Name    string
	Path    string
	Version string
}

// Found reports whether the tool is available.
func (t Tool) Found() bool { return t.Path != "" }

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// Find looks up a program in PATH and asks it for its version.
//
// A tool that is present but will not report a version is still reported as
// found, with an empty Version.
func Find(name string, versionArgs ...string) Tool {
	path, err := exec.LookPath(name)
	if err != nil {
		return Tool{Name: name}
	}
	t := Tool{Name: name, Path: path}
	if len(versionArgs) == 0 {
		versionArgs = []string{"--version"}
	}
	out, err := exec.Command(path, versionArgs...).Output()
	if err != nil {
		return t
	}
	first := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	if m := versionRe.FindString(first); m != "" {
		t.Version = m
	}
	return t
}

// CMake locates cmake.
func CMake() Tool { return Find("cmake") }

// Ninja locates ninja.
func Ninja() Tool { return Find("ninja") }

// Git locates git.
func Git() Tool { return Find("git") }

// Compiler locates a C++ compiler, preferring the CXX environment variable.
func Compiler() Tool {
	if cxx := strings.TrimSpace(os.Getenv("CXX")); cxx != "" {
		if t := Find(cxx); t.Found() {
			return t
		}
	}
	// Concrete drivers first. "c++" is usually a symlink to one of these, and
	// writing the real name into cppm.json tells the reader which compiler they
	// are actually getting; "c++" hides it.
	for _, name := range []string{"g++", "clang++", "c++"} {
		if t := Find(name); t.Found() {
			return t
		}
	}
	return Tool{Name: "c++"}
}

// CompareVersions compares two dotted version strings.
// It returns -1 if a < b, 0 if equal, and 1 if a > b.
//
// This is for *tool* versions, which are well-behaved. It is deliberately not
// used for dependency versions: those are opaque git refs that cppm never
// compares. See docs/architecture.md section 3b.
func CompareVersions(a, b string) int {
	pa, pb := splitVersion(a), splitVersion(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func splitVersion(s string) []int {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

// Run executes a program with its output connected to cppm's own streams.
//
// Nothing is passed through a shell: args is an argv slice.
func Run(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			return fmt.Errorf("%s exited with status %d", name, exitErr.ExitCode())
		}
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}

// Capture executes a program and returns its stdout.
func Capture(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", name, lastLine(msg))
	}
	return string(out), nil
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// StandardSupport reports whether a compiler accepts a given C++ standard.
//
// Rather than maintaining a table of which compiler version gained which
// standard -- which goes stale and is wrong for vendor forks -- this asks the
// compiler directly by syntax-checking an empty translation unit. The compiler
// is the authority on its own flags.
//
// ok is meaningless when known is false, which happens for compilers that do
// not take GCC-style flags (notably MSVC).
func StandardSupport(cxx string, std int) (ok bool, known bool) {
	path, err := exec.LookPath(cxx)
	if err != nil {
		return false, false
	}
	if !gnuStyleFlags(path) {
		return false, false
	}
	cmd := exec.Command(path, fmt.Sprintf("-std=c++%d", std), "-fsyntax-only", "-x", "c++", "-")
	cmd.Stdin = strings.NewReader("")
	return cmd.Run() == nil, true
}

// gnuStyleFlags reports whether a compiler takes -std=c++NN, as GCC and Clang
// do. MSVC uses /std:c++NN and is excluded.
func gnuStyleFlags(path string) bool {
	base := strings.ToLower(filepathBase(path))
	for _, msvc := range []string{"cl.exe", "cl"} {
		if base == msvc {
			return false
		}
	}
	// A compiler that rejects -std=c++17 outright is not GCC-style.
	cmd := exec.Command(path, "-std=c++17", "-fsyntax-only", "-x", "c++", "-")
	cmd.Stdin = strings.NewReader("")
	return cmd.Run() == nil
}

func filepathBase(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// CTest is CMake's test driver. It ships with CMake, so a missing ctest beside
// a present cmake means a broken installation rather than a missing optional
// tool.
func CTest() Tool { return Find("ctest") }
