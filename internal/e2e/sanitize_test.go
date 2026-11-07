package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useAfterFree is a bug ASan reports unambiguously. A leak is a worse choice
// for a test: at -O1 the pointer is often still live in a register at exit, so
// LeakSanitizer calls it "still reachable" and says nothing.
const useAfterFree = `#include <cstdlib>
#include <cstdio>
int main() {
    int* p = (int*)malloc(4 * sizeof(int));
    p[0] = 7;
    free(p);
    printf("%d\n", p[0]);
    return 0;
}
`

const signedOverflow = `#include <climits>
#include <cstdio>
int add(int a, int b) { return a + b; }
int main() {
    printf("%d\n", add(INT_MAX, 1));
    return 0;
}
`

const dataRace = `#include <thread>
#include <cstdio>
static int shared = 0;
int main() {
    std::thread a([]{ for (int i = 0; i < 1000; ++i) shared++; });
    std::thread b([]{ for (int i = 0; i < 1000; ++i) shared++; });
    a.join(); b.join();
    printf("%d\n", shared);
    return 0;
}
`

// sanitizerProject scaffolds a project whose test executable contains body.
func sanitizerProject(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "init", name, "--with-tests")
	proj := filepath.Join(dir, name)
	mustWrite(t, filepath.Join(proj, "tests", name+"_test.cpp"), body)
	return proj
}

// Each sanitizer has to actually catch the class of bug it claims, and the
// failure has to fail the command. A sanitizer run that reports and exits 0
// means CI goes green with the bug still in it, which is worse than not
// running it at all.
func TestSanitizersCatchTheirOwnBugClass(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "ctest")
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
			proj := sanitizerProject(t, "San", c.body)
			out, err := run(t, proj, "test", "--sanitize="+c.sanitize)
			if err == nil {
				t.Fatalf("--sanitize=%s did not fail on a bug it is meant to catch:\n%s", c.sanitize, out)
			}
			if !strings.Contains(out, c.expect) {
				t.Errorf("--sanitize=%s did not report %q:\n%s", c.sanitize, c.expect, out)
			}
		})
	}
}

// The clean case: a correct program must pass under every sanitizer. A
// sanitizer that fails a working build is one nobody will turn on.
func TestSanitizersPassOnCorrectCode(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "ctest")
	requireGNUStyleCompiler(t)

	for _, san := range []string{"asan", "ubsan", "asan+ubsan", "tsan"} {
		t.Run(san, func(t *testing.T) {
			dir := t.TempDir()
			mustRun(t, dir, "init", "Clean", "--with-tests")
			proj := filepath.Join(dir, "Clean")
			out, err := run(t, proj, "test", "--sanitize="+san)
			if err != nil {
				t.Fatalf("the scaffolded suite must pass under --sanitize=%s:\n%s", san, out)
			}
			if !strings.Contains(out, "100% tests passed") {
				t.Errorf("expected a clean run under --sanitize=%s:\n%s", san, out)
			}
		})
	}
}

// The inverse of TestStrictWarningsDoNotReachADependency, and the pair is the
// point of the design. ASan and TSan instrument the whole program or they are
// unreliable, so here the flags MUST reach the dependency -- which works only
// because cppm builds every dependency from source in the same tree.
func TestSanitizerFlagsReachADependency(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireGNUStyleCompiler(t)

	dir := t.TempDir()
	dep := filepath.Join(dir, "deplib")
	mustMkdirAll(t, filepath.Join(dep, "src"))
	mustWrite(t, filepath.Join(dep, "CMakeLists.txt"), `cmake_minimum_required(VERSION 3.24)
project(deplib LANGUAGES CXX)
add_library(deplib src/dep.cpp)
`)
	mustWrite(t, filepath.Join(dep, "src", "dep.cpp"), "int dep_value() { return 41; }\n")

	mustRun(t, dir, "init", "Instrumented")
	proj := filepath.Join(dir, "Instrumented")
	mustRun(t, proj, "add", "local:../deplib", "--target", "deplib")
	mustWrite(t, filepath.Join(proj, "src", "main.cpp"), `int dep_value();
int main() { return dep_value() == 41 ? 0 : 1; }
`)
	mustRun(t, proj, "build", "--sanitize=asan")

	flags := compileFlagsFor(t, proj, "asan")
	cmd, ok := flags["dep.cpp"]
	if !ok {
		t.Fatalf("the dependency's source is not in compile_commands.json, so this test proves nothing; got %v", keys(flags))
	}
	if !strings.Contains(cmd, "-fsanitize=address") {
		t.Errorf("the dependency was NOT instrumented, so ASan cannot see its allocations:\n%s", cmd)
	}
	if own := flags["main.cpp"]; !strings.Contains(own, "-fsanitize=address") {
		t.Errorf("the project's own source was not instrumented:\n%s", own)
	}
}

// A sanitizer build gets its own tree. Sharing one with debug would mix
// instrumented and uninstrumented objects, which links successfully and then
// crashes inside the sanitizer's own runtime -- a long way from the bug the
// user was looking for.
func TestSanitizerBuildsIntoItsOwnTree(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireGNUStyleCompiler(t)

	dir := t.TempDir()
	mustRun(t, dir, "init", "Trees")
	proj := filepath.Join(dir, "Trees")
	mustRun(t, proj, "build")
	mustRun(t, proj, "build", "--sanitize=tsan")

	for _, preset := range []string{"debug", "tsan"} {
		if _, err := os.Stat(filepath.Join(proj, "build", preset)); err != nil {
			t.Errorf("no build tree for %q: %v", preset, err)
		}
	}
	// And the debug tree must be untouched by the sanitizer run.
	debug := compileFlagsFor(t, proj, "debug")["main.cpp"]
	if strings.Contains(debug, "-fsanitize") {
		t.Errorf("the sanitizer leaked into the debug build tree:\n%s", debug)
	}
	// _FORTIFY_SOURCE must stay out of a sanitizer build: fortified libc calls
	// hide the overflows ASan exists to find.
	tsan := compileFlagsFor(t, proj, "tsan")["main.cpp"]
	if strings.Contains(tsan, "_FORTIFY_SOURCE") {
		t.Errorf("_FORTIFY_SOURCE is in the sanitizer build, where it hides the bugs being hunted:\n%s", tsan)
	}
	if !strings.Contains(tsan, "-O1") {
		t.Errorf("expected the sanitizer build at -O1:\n%s", tsan)
	}
}

// The combination the roadmap says cppm must refuse. A sanitizer-instrumented
// binary under valgrind produces output that means nothing, so cppm declines
// rather than producing it.
func TestSanitizerAndValgrindAreRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cppm")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Refused")
	proj := filepath.Join(dir, "Refused")

	out, err := run(t, proj, "test", "--sanitize=asan", "--memcheck")
	if err == nil {
		t.Fatalf("--sanitize with --memcheck must be refused:\n%s", out)
	}
	// The refusal has to say why, or the user works around it.
	for _, want := range []string{"cannot be combined", "intercept the same allocations"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, out)
		}
	}

	// And two valgrind tools at once, which valgrind itself cannot do.
	if out, err := run(t, proj, "test", "--memcheck", "--helgrind"); err == nil {
		t.Errorf("--memcheck with --helgrind must be refused:\n%s", out)
	}
}

// --release and --sanitize cannot be combined: a sanitizer preset has its own
// tree and its own -g -O1, so silently ignoring --release would hand the user
// an unoptimised build they believe is a release one.
func TestReleaseAndSanitizeAreRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cppm")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Excl")
	proj := filepath.Join(dir, "Excl")
	if out, err := run(t, proj, "build", "--release", "--sanitize=asan"); err == nil {
		t.Errorf("--release with --sanitize must be refused:\n%s", out)
	}
}

// ASan and TSan cannot be combined, and the refusal has to be a sentence
// rather than a list of valid values -- it is the one combination people
// actually try.
func TestAsanAndTsanRefusalExplainsItself(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cppm")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Mutual")
	proj := filepath.Join(dir, "Mutual")

	out, err := run(t, proj, "build", "--sanitize=asan+tsan")
	if err == nil {
		t.Fatalf("--sanitize=asan+tsan must be refused:\n%s", out)
	}
	for _, want := range []string{"cannot be combined", "two passes"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, out)
		}
	}
}

// init scaffolds the suppressions file, because the first memcheck run on any
// real project reports errors inside glibc that are not the user's to fix.
func TestInitScaffoldsValgrindSuppressions(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cppm")
	}
	dir := t.TempDir()
	mustRun(t, dir, "init", "Supp")
	supp := readFile(t, filepath.Join(dir, "Supp", "valgrind.supp"))
	if !strings.Contains(supp, "--gen-suppressions") {
		t.Errorf("the scaffolded suppressions file does not say how to generate entries:\n%s", supp)
	}
}

// Without valgrind installed, the refusal has to name what to use instead.
// Valgrind is effectively Linux-only, so "not found" on its own leaves a macOS
// user installing something that will not work.
func TestMissingValgrindRecommendsTheSanitizers(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cppm")
	}
	requireTool(t, "cmake")
	if _, err := os.Stat("/usr/bin/valgrind"); err == nil {
		t.Skip("valgrind is installed, so this path is not reachable here")
	}

	dir := t.TempDir()
	mustRun(t, dir, "init", "NoVg")
	proj := filepath.Join(dir, "NoVg")

	out, err := run(t, proj, "test", "--memcheck")
	if err == nil {
		t.Skip("valgrind appears to be available after all")
	}
	if !strings.Contains(out, "sanitize") {
		t.Errorf("the missing-valgrind message does not point at the sanitizers:\n%s", out)
	}
}

// With valgrind present, memcheck has to actually run and report a real leak.
// Skipped where valgrind is absent rather than faked.
func TestMemcheckFindsALeak(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "ctest")
	requireTool(t, "valgrind")
	requireGNUStyleCompiler(t)

	proj := sanitizerProject(t, "Vg", `#include <cstdlib>
#include <cstdio>
int main() {
    int* p = (int*)malloc(64);
    p[0] = 1;
    printf("%d\n", p[0]);
    p = nullptr;        // the only reference is dropped: a definite leak
    return 0;
}
`)
	out, err := run(t, proj, "test", "--memcheck")
	if err == nil {
		t.Fatalf("memcheck did not fail on a definite leak -- is --error-exitcode=1 reaching valgrind?\n%s", out)
	}
	if !strings.Contains(out, "valgrind") && !strings.Contains(out, "Memory") {
		t.Errorf("the output does not look like a memcheck run:\n%s", out)
	}
}

// Helgrind's advantage over TSan is lock-order checking: it reports that one
// thread takes A then B while another takes B then A, which is a deadlock
// waiting for the right timing even though it has never actually deadlocked.
//
// This program never deadlocks and passes an ordinary test run, which is the
// point -- nothing else in cppm's toolbox finds it.
func TestHelgrindFindsALockOrderInversion(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "ctest")
	requireTool(t, "valgrind")
	requireGNUStyleCompiler(t)

	proj := sanitizerProject(t, "Lock", `#include <thread>
#include <mutex>
#include <cstdio>
static std::mutex a, b;
int main() {
    std::thread t1([]{ std::lock_guard<std::mutex> la(a); std::lock_guard<std::mutex> lb(b); });
    t1.join();
    std::thread t2([]{ std::lock_guard<std::mutex> lb(b); std::lock_guard<std::mutex> la(a); });
    t2.join();
    printf("done\n");
    return 0;
}
`)
	// It passes normally: the inversion is real but never trips.
	if out, err := run(t, proj, "test"); err != nil {
		t.Fatalf("this program does not deadlock and must pass an ordinary run:\n%s", out)
	}

	out, err := run(t, proj, "test", "--helgrind")
	if err == nil {
		t.Fatalf("--helgrind did not fail on a lock-order inversion:\n%s", out)
	}
	// The finding itself is in the per-test log, which is where cppm points.
	log := readFile(t, filepath.Join(proj, "build", "debug", "Testing", "Temporary", "MemoryChecker.1.log"))
	if !strings.Contains(log, "lock order") {
		t.Errorf("helgrind did not report a lock-order violation:\n%s", log)
	}
}

// A profiler produces a report, not a verdict. CTest counts "defects" by
// parsing memcheck's output format, so it reports a massif run as clean no
// matter what -- cppm has to say where the output went instead of repeating a
// meaningless zero.
func TestProfilersSucceedAndNameTheirOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "ctest")
	requireTool(t, "valgrind")
	requireGNUStyleCompiler(t)

	cases := []struct{ flag, file, reader string }{
		{"--massif", "massif.out.", "ms_print"},
		{"--cachegrind", "cachegrind.out.", "cg_annotate"},
	}
	for _, c := range cases {
		t.Run(c.flag, func(t *testing.T) {
			dir := t.TempDir()
			mustRun(t, dir, "init", "Prof", "--with-tests")
			proj := filepath.Join(dir, "Prof")

			out, err := run(t, proj, "test", c.flag)
			if err != nil {
				t.Fatalf("%s must not fail on a correct program:\n%s", c.flag, out)
			}
			// Not reported as a clean check, because it checked nothing.
			if !strings.Contains(out, "writes no verdict") {
				t.Errorf("%s was reported as if it had checked for errors:\n%s", c.flag, out)
			}
			if !strings.Contains(out, c.reader) {
				t.Errorf("%s does not say how to read its output (%s):\n%s", c.flag, c.reader, out)
			}
			// And the profile really has to be there.
			matches, _ := filepath.Glob(filepath.Join(proj, "build", "debug", c.file+"*"))
			if len(matches) == 0 {
				t.Errorf("%s produced no %s* file in the build tree", c.flag, c.file)
			}
		})
	}
}

// --gen-suppressions has to produce something pasteable, because writing
// valgrind's stack-frame syntax by hand is what stops people using
// suppressions at all.
func TestGenSuppressionsProducesAPasteableEntry(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "ctest")
	requireTool(t, "valgrind")
	requireGNUStyleCompiler(t)

	proj := sanitizerProject(t, "Supp", `#include <cstdlib>
#include <cstdio>
int main() {
    int* p = (int*)malloc(64);
    p[0] = 1;
    printf("%d\n", p[0]);
    p = nullptr;
    return 0;
}
`)
	// Expected to fail: there is a real leak. The suppression block is the
	// artefact under test.
	if _, err := run(t, proj, "test", "--memcheck", "--gen-suppressions"); err == nil {
		t.Fatal("the leak should still have failed the run")
	}
	log := readFile(t, filepath.Join(proj, "build", "debug", "Testing", "Temporary", "MemoryChecker.1.log"))
	for _, want := range []string{"insert_a_suppression_name_here", "Memcheck:Leak", "fun:malloc"} {
		if !strings.Contains(log, want) {
			t.Errorf("the generated suppression does not contain %q:\n%s", want, log)
		}
	}
}

// --gen-suppressions with a profiler is meaningless: there are no reported
// errors to suppress.
func TestGenSuppressionsRefusedForAProfiler(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cppm")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "NoSupp")
	proj := filepath.Join(dir, "NoSupp")
	out, err := run(t, proj, "test", "--massif", "--gen-suppressions")
	if err == nil {
		t.Fatalf("--gen-suppressions with --massif must be refused:\n%s", out)
	}
	if !strings.Contains(out, "profiler") {
		t.Errorf("the refusal does not explain why:\n%s", out)
	}
}

// Pointing valgrind at a sanitizer-instrumented tree is refused by the
// generated CMake as well as by the CLI, so a bare ctest cannot do it either.
func TestInstrumentedTreeRefusesABareCtestMemcheck(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "ctest")
	requireTool(t, "valgrind")
	requireGNUStyleCompiler(t)

	dir := t.TempDir()
	mustRun(t, dir, "init", "Both", "--with-tests")
	proj := filepath.Join(dir, "Both")

	out := mustRun(t, proj, "build", "--sanitize=asan")
	if !strings.Contains(out, "valgrind disabled in this build tree") {
		t.Errorf("configuring an instrumented tree must disable valgrind, and say so:\n%s", out)
	}

	cache := readFile(t, filepath.Join(proj, "build", "asan", "CMakeCache.txt"))
	for _, line := range strings.Split(cache, "\n") {
		if strings.HasPrefix(line, "MEMORYCHECK_COMMAND:") && !strings.HasSuffix(strings.TrimSpace(line), "=") {
			t.Errorf("an instrumented build tree still has a memory checker set, so 'ctest -T memcheck' would run it: %s", line)
		}
	}
}
