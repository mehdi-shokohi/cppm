package e2e

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runProject scaffolds a project with NO test suite and puts body in main.cpp.
//
// Deliberately `init` without --with-tests: the point of every test in this
// file is that the safety tooling reaches the real program, so a test target
// being present would hide a regression that made run depend on one.
func runProject(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "init", name)
	proj := filepath.Join(dir, name)
	mustWrite(t, filepath.Join(proj, "src", "main.cpp"), body)
	return proj
}

// exitCodeOf reads a child process's exit status out of an error from run().
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("not an exit error: %v", err)
	}
	return ee.ExitCode()
}

const echoArgsAndFail = `#include <cstdio>
int main(int argc, char** argv) {
    for (int i = 1; i < argc; ++i) printf("arg[%d]=%s\n", i, argv[i]);
    return 3;
}
`

const nullDeref = `#include <cstdio>
int main() {
    int* p = nullptr;
    printf("%d\n", *p);
    return 0;
}
`

// A leak reported by valgrind but not by LeakSanitizer at -O1: the block is
// written to, so it cannot be optimised away, and nothing holds the pointer at
// exit.
const definiteLeak = `#include <cstdlib>
#include <cstdio>
static void leak() {
    int* p = (int*)malloc(64);
    p[0] = 7;
    printf("%d\n", p[0]);
}
int main() { leak(); return 0; }
`

// The headline case: a project with no test suite at all builds and runs.
//
// This is what the command exists for. The sanitizers instrument every line the
// compiler sees, including main(), so they never needed a test suite to be
// useful -- only a way to be launched.
func TestRunBuildsAndRunsAProjectWithNoTests(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Runnable")
	proj := filepath.Join(dir, "Runnable")

	// No test target exists, and none is created.
	if _, err := os.Stat(filepath.Join(proj, "tests")); err == nil {
		t.Fatal("init without --with-tests should scaffold no test directory")
	}

	out := mustRun(t, proj, "run")
	if !strings.Contains(out, "Hello from Runnable") {
		t.Errorf("the program did not run:\n%s", out)
	}
}

// Everything after -- reaches the program unchanged, and the program's exit
// status becomes cppm's.
//
// A runner that collapsed every status to 1 would be useless in a script, and
// one that ate the arguments would be useless for anything taking a config
// file.
func TestRunPassesArgumentsAndExitStatusThrough(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")

	proj := runProject(t, "Args", echoArgsAndFail)
	// --verbose and -x are cppm-shaped arguments: after --, they are the
	// program's and must not be parsed by cppm.
	out, err := run(t, proj, "run", "--", "--verbose", "-x", "two words")
	if code := exitCodeOf(t, err); code != 3 {
		t.Errorf("cppm exited %d, want the program's own 3:\n%s", code, out)
	}
	for _, want := range []string{"arg[1]=--verbose", "arg[2]=-x", "arg[3]=two words"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q from the program's output:\n%s", want, out)
		}
	}
}

// A bare positional is ambiguous between a target name and a program argument,
// so it is refused with both spellings rather than guessed at.
func TestRunRefusesABarePositionalArgument(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cmake")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Bare")
	proj := filepath.Join(dir, "Bare")

	out, err := run(t, proj, "run", "something")
	if err == nil {
		t.Fatalf("a bare positional argument should be refused:\n%s", out)
	}
	for _, want := range []string{"--target something", "-- something"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal should offer %q:\n%s", want, out)
		}
	}
}

// Each sanitizer catches its own bug class in main(), with no test target in
// the project.
//
// The assertion that the command FAILS is the one that matters, and it is what
// proves the runtime options were applied: a sanitizer left on its defaults
// prints its report and exits 0. Those options live in the test preset's
// environment, which has no effect on a binary cppm launches itself, so run
// has to set them -- and if it ever stops, this test goes red rather than
// quietly passing a broken feature.
func TestSanitizersCatchBugsInMainWithoutATestSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireGNUStyleCompiler(t)

	cases := []struct {
		sanitize string
		body     string
		expect   string
	}{
		{"asan", useAfterFree, "heap-use-after-free"},
		{"ubsan", signedOverflow, "signed integer overflow"},
		{"asan+ubsan", signedOverflow, "signed integer overflow"},
		{"tsan", dataRace, "data race"},
	}
	for _, c := range cases {
		t.Run(c.sanitize, func(t *testing.T) {
			proj := runProject(t, "San", c.body)
			out, err := run(t, proj, "run", "--sanitize="+c.sanitize)
			if err == nil {
				t.Fatalf("--sanitize=%s did not fail on a bug it is meant to catch -- are the\n"+
					"runtime options reaching the program?\n%s", c.sanitize, out)
			}
			if !strings.Contains(out, c.expect) {
				t.Errorf("--sanitize=%s did not report %q:\n%s", c.sanitize, c.expect, out)
			}
		})
	}
}

// A correct program must pass under every sanitizer, or nobody will turn one
// on.
func TestRunPassesOnCorrectCodeUnderEverySanitizer(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireGNUStyleCompiler(t)

	for _, san := range []string{"asan", "ubsan", "asan+ubsan", "tsan"} {
		t.Run(san, func(t *testing.T) {
			dir := t.TempDir()
			mustRun(t, dir, "init", "Clean")
			proj := filepath.Join(dir, "Clean")
			out, err := run(t, proj, "run", "--sanitize="+san)
			if err != nil {
				t.Fatalf("the scaffolded program must run clean under --sanitize=%s:\n%s", san, out)
			}
			if !strings.Contains(out, "Hello from Clean") {
				t.Errorf("the program did not run under --sanitize=%s:\n%s", san, out)
			}
		})
	}
}

// A sanitizer finding arrives as SIGABRT, and the message has to say that is
// what it is -- "aborted" alone reads as a bug in the program rather than as
// the sanitizer doing its job.
func TestASanitizerAbortIsExplained(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireGNUStyleCompiler(t)

	proj := runProject(t, "Abort", useAfterFree)
	out, err := run(t, proj, "run", "--sanitize=asan")
	if err == nil {
		t.Fatalf("expected a failure:\n%s", out)
	}
	for _, want := range []string{"abort_on_error=1", "file and line"} {
		if !strings.Contains(out, want) {
			t.Errorf("the message should explain the abort, mentioning %q:\n%s", want, out)
		}
	}
}

// memcheck against the real program, with no test target anywhere.
//
// The counterpart to TestMemcheckFindsALeak, which goes through CTest. This
// path does not: cppm hands valgrind the binary directly, so there is no
// DartConfiguration.tcl and no "-T memcheck exits 0 having found no tests"
// hazard -- valgrind's own exit status is the result. That is the thing to
// prove here.
func TestRunMemcheckFindsALeakInMain(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler and valgrind")
	}
	requireTool(t, "cmake")
	requireTool(t, "valgrind")
	requireGNUStyleCompiler(t)

	proj := runProject(t, "Leaky", definiteLeak)
	out, err := run(t, proj, "run", "--memcheck")
	if err == nil {
		t.Fatalf("memcheck did not fail on a definite leak -- is --error-exitcode=1 reaching valgrind?\n%s", out)
	}
	if !strings.Contains(out, "definitely lost") {
		t.Errorf("the output does not look like a memcheck run:\n%s", out)
	}
	// The run has to be identifiable as a valgrind run before it starts, since
	// it is 20-100x slower and otherwise looks like a hang.
	if !strings.Contains(out, "20-100x slower") {
		t.Errorf("a valgrind run must warn how slow it is, before running:\n%s", out)
	}
}

// --gen-suppressions has to work on the direct-run path too: writing valgrind's
// stack-frame syntax by hand is what stops people using suppressions at all.
func TestRunGenSuppressionsProducesAPasteableEntry(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler and valgrind")
	}
	requireTool(t, "cmake")
	requireTool(t, "valgrind")
	requireGNUStyleCompiler(t)

	proj := runProject(t, "Supp", definiteLeak)
	out, _ := run(t, proj, "run", "--memcheck", "--gen-suppressions")
	for _, want := range []string{"insert_a_suppression_name_here", "Memcheck:Leak"} {
		if !strings.Contains(out, want) {
			t.Errorf("no pasteable suppression in the output; expected %q:\n%s", want, out)
		}
	}
}

// A profiler writes no verdict, so a successful run otherwise looks like it did
// nothing. cppm has to say where the output went.
func TestRunUnderMassifNamesItsOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler and valgrind")
	}
	requireTool(t, "cmake")
	requireTool(t, "valgrind")
	requireGNUStyleCompiler(t)

	proj := runProject(t, "Prof", definiteLeak)
	out, err := run(t, proj, "run", "--massif")
	if err != nil {
		t.Fatalf("a profiler run reports no errors and must succeed:\n%s", out)
	}
	if !strings.Contains(out, "ms_print") {
		t.Errorf("the output does not say how to read the profile:\n%s", out)
	}
	matches, _ := filepath.Glob(filepath.Join(proj, "massif.out.*"))
	if len(matches) == 0 {
		t.Errorf("massif wrote no profile into the project root:\n%s", out)
	}
}

// The combination cppm must refuse: a sanitizer-instrumented binary under
// valgrind produces output that means nothing, because the two intercept the
// same allocations.
func TestRunRefusesASanitizerUnderValgrind(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cmake")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Both")
	proj := filepath.Join(dir, "Both")

	out, err := run(t, proj, "run", "--sanitize=asan", "--memcheck")
	if err == nil {
		t.Fatalf("--sanitize with --memcheck must be refused:\n%s", out)
	}
	// The refusal has to name run, not test: a suggested command the user
	// cannot retype is no help.
	if !strings.Contains(out, "cppm run --sanitize=asan") {
		t.Errorf("the refusal should suggest the two passes as run commands:\n%s", out)
	}
	if out, err := run(t, proj, "run", "--memcheck", "--helgrind"); err == nil {
		t.Errorf("--memcheck with --helgrind must be refused:\n%s", out)
	}
}

// A crash under a plain run is the one case where cppm knows the next step.
func TestRunCrashPointsAtTheSanitizers(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireGNUStyleCompiler(t)

	proj := runProject(t, "Crash", nullDeref)
	out, err := run(t, proj, "run")
	if err == nil {
		t.Fatalf("a segfaulting program must fail the command:\n%s", out)
	}
	for _, want := range []string{"crashed", "--sanitize=asan+ubsan", "--memcheck"} {
		if !strings.Contains(out, want) {
			t.Errorf("the crash message should mention %q:\n%s", want, out)
		}
	}
}

// With two executables declared, cppm declines rather than picking one. A
// confidently wrong choice wastes a 20-100x valgrind run and teaches the user
// to stop reading what was picked.
func TestRunDeclinesToChooseBetweenExecutables(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cmake")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Two")
	proj := filepath.Join(dir, "Two")

	// A second executable, added the way a user would add one: to their own
	// CMakeLists.txt, which cppm does not rewrite, and then declared in
	// cppm.json so cppm knows it exists.
	mustWrite(t, filepath.Join(proj, "src", "tool.cpp"),
		"#include <cstdio>\nint main() { printf(\"tools_cli here\\n\"); return 0; }\n")
	lists := readFile(t, filepath.Join(proj, "CMakeLists.txt"))
	mustWrite(t, filepath.Join(proj, "CMakeLists.txt"),
		lists+"\nadd_executable(tools_cli src/tool.cpp)\n")
	m := readFile(t, filepath.Join(proj, "cppm.json"))
	m = strings.Replace(m, `"targets": {`,
		`"targets": {
    "tools_cli": { "kind": "executable", "sources": ["src/tool.cpp"] },`, 1)
	mustWrite(t, filepath.Join(proj, "cppm.json"), m)

	out, err := run(t, proj, "run", "--no-build")
	if err == nil {
		t.Fatalf("two executables must not be resolved silently:\n%s", out)
	}
	for _, want := range []string{"Two", "tools_cli", "--target"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, out)
		}
	}

	// Named explicitly, it runs -- and it is the named binary that runs, not
	// the project's other one.
	out = mustRun(t, proj, "run", "--target", "tools_cli")
	if !strings.Contains(out, "tools_cli here") {
		t.Errorf("--target should have run tools_cli:\n%s", out)
	}
	if strings.Contains(out, "Hello from Two") {
		t.Errorf("--target tools_cli ran the wrong executable:\n%s", out)
	}
}

// run appears in the help, because a command nobody can find is a command
// nobody uses -- and this one is the answer to "how do I check the real
// program".
func TestRunIsDocumentedInTheHelp(t *testing.T) {
	dir := t.TempDir()
	out := mustRun(t, dir, "help")
	if !strings.Contains(out, "run") {
		t.Errorf("run is missing from the command list:\n%s", out)
	}
	out = mustRun(t, dir, "help", "run")
	for _, want := range []string{"--sanitize", "--memcheck", "-- ", "exit status"} {
		if !strings.Contains(out, want) {
			t.Errorf("'cppm help run' should mention %q:\n%s", want, out)
		}
	}
}

// Asking for a memory checker in a project with no tests is answered with the
// command that needs none.
//
// The gap this closes: `cppm test --memcheck` on a project without a test suite
// could only say "write some tests", which is the wrong answer to "my program
// leaks". The instrumentation never needed a suite -- only a runner.
func TestTestWithoutTestsPointsAtRunForASafetyTool(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "ctest")

	dir := t.TempDir()
	mustRun(t, dir, "init", "NoTests")
	proj := filepath.Join(dir, "NoTests")

	cases := []struct{ flag, want string }{
		{"--memcheck", "cppm run --memcheck"},
		{"--sanitize=asan+ubsan", "cppm run --sanitize=asan+ubsan"},
	}
	for _, c := range cases {
		t.Run(c.flag, func(t *testing.T) {
			out, err := run(t, proj, "test", c.flag)
			if err == nil {
				t.Fatalf("a project with no tests should not report success:\n%s", out)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("the refusal should offer %q:\n%s", c.want, out)
			}
		})
	}

	// A plain test run has no equivalent to suggest, so it does not.
	out, err := run(t, proj, "test")
	if err == nil {
		t.Fatalf("expected a failure:\n%s", out)
	}
	if strings.Contains(out, "cppm run") {
		t.Errorf("a plain test run should not be answered with a run command:\n%s", out)
	}
}
