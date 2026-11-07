package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// compileFlagsFor returns the compiler command lines from a configured build
// tree, keyed by source file name.
//
// compile_commands.json is the only honest place to check this: it is what the
// compiler is actually invoked with, not what cppm meant to emit. The unit
// tests already cover the text of the generated CMake.
func compileFlagsFor(t *testing.T, proj, preset string) map[string]string {
	t.Helper()
	path := filepath.Join(proj, "build", preset, "compile_commands.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no compile_commands.json, so the real flags cannot be checked: %v", err)
	}
	var entries []struct {
		File      string   `json:"file"`
		Command   string   `json:"command"`
		Arguments []string `json:"arguments"`
	}
	if err := json.Unmarshal(b, &entries); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		cmd := e.Command
		if cmd == "" {
			cmd = strings.Join(e.Arguments, " ")
		}
		out[filepath.Base(e.File)] = cmd
	}
	return out
}

func requireGNUStyleCompiler(t *testing.T) {
	t.Helper()
	// The flags under test are GCC and Clang spellings. MSVC gets its own
	// set, which this check would report as missing.
	if runtime.GOOS == "windows" {
		t.Skip("checks GCC/Clang flag spellings")
	}
}

// Warnings are on out of the box. A tool that generates the build has no
// excuse for generating one with the warnings off, and -Wall -Wextra cannot
// fail a build on their own.
func TestNewProjectCompilesWithWarningsOn(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireGNUStyleCompiler(t)

	dir := t.TempDir()
	mustRun(t, dir, "init", "Warned", "--with-tests")
	proj := filepath.Join(dir, "Warned")
	mustRun(t, proj, "build")

	flags := compileFlagsFor(t, proj, "debug")
	if len(flags) == 0 {
		t.Fatal("no compile commands at all")
	}
	for file, cmd := range flags {
		for _, want := range []string{"-Wall", "-Wextra"} {
			if !strings.Contains(cmd, want) {
				t.Errorf("%s is compiled without %s:\n%s", file, want, cmd)
			}
		}
		// Nothing that can fail a build, at the default level.
		if strings.Contains(cmd, "-Werror") {
			t.Errorf("%s is compiled with -Werror at the default level:\n%s", file, cmd)
		}
	}
}

// The whole point of applying the flags per target. A dependency's warnings
// are not the user's bug, and a dependency built with -Werror is a build that
// an upstream release can stop without the user changing a line.
//
// Checked against a real dependency with real warnings in it, because the unit
// test can only prove that add_compile_options() is absent from the generated
// text -- not that nothing else carries the flags down.
func TestStrictWarningsDoNotReachADependency(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireGNUStyleCompiler(t)

	dir := t.TempDir()

	// A path dependency, so this test needs no network. Its source warns
	// under -Wall -Wextra: an unused parameter and an unused variable.
	dep := filepath.Join(dir, "sloppylib")
	mustMkdirAll(t, filepath.Join(dep, "src"))
	mustWrite(t, filepath.Join(dep, "CMakeLists.txt"), `cmake_minimum_required(VERSION 3.24)
project(sloppylib LANGUAGES CXX)
add_library(sloppy src/sloppy.cpp)
`)
	mustWrite(t, filepath.Join(dep, "src", "sloppy.cpp"), `int sloppy_value(int unused_param) {
    int never_read = 7;
    return 41;
}
`)

	mustRun(t, dir, "init", "Strictly")
	proj := filepath.Join(dir, "Strictly")
	mustRun(t, proj, "add", "local:../sloppylib", "--target", "sloppy")
	mustWrite(t, filepath.Join(proj, "src", "main.cpp"), `int sloppy_value(int);
int main() { return sloppy_value(1) == 41 ? 0 : 1; }
`)

	// -Werror on everything in sight would fail here if the flags leaked.
	out, err := run(t, proj, "build", "--warnings=error")
	if err != nil {
		t.Fatalf("--warnings=error failed, which means the flags reached the dependency:\n%s", out)
	}

	flags := compileFlagsFor(t, proj, "debug")
	if cmd, ok := flags["main.cpp"]; !ok {
		t.Fatal("the project's own source is not in compile_commands.json")
	} else if !strings.Contains(cmd, "-Werror") {
		t.Errorf("the project's own source was not compiled with -Werror:\n%s", cmd)
	}
	// Asserted rather than skipped if absent: a compile_commands.json without
	// the dependency in it would make this test pass while checking nothing.
	cmd, ok := flags["sloppy.cpp"]
	if !ok {
		t.Fatalf("the dependency's source is not in compile_commands.json, so this test proves nothing; got %v", keys(flags))
	}
	{
		for _, forbidden := range []string{"-Werror", "-Wall", "-Wextra"} {
			if strings.Contains(cmd, forbidden) {
				t.Errorf("the dependency was compiled with %s, which is not the user's to fix:\n%s", forbidden, cmd)
			}
		}
	}
}

// -Werror has to actually fail on the user's own code, or the level is
// decoration.
func TestErrorLevelFailsOnTheProjectsOwnWarning(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireGNUStyleCompiler(t)

	dir := t.TempDir()
	mustRun(t, dir, "init", "Erroring")
	proj := filepath.Join(dir, "Erroring")
	mustWrite(t, filepath.Join(proj, "src", "main.cpp"), `int main() {
    int never_read = 41;
    return 0;
}
`)

	// The same code must build at the default level and fail at error, which
	// is what distinguishes the two.
	if out, err := run(t, proj, "build"); err != nil {
		t.Fatalf("an unused variable must not fail the default level:\n%s", out)
	}
	out, err := run(t, proj, "build", "--warnings=error")
	if err == nil {
		t.Fatalf("--warnings=error did not fail on an unused variable:\n%s", out)
	}
	if !strings.Contains(out, "never_read") {
		t.Errorf("the failure does not name the offending variable:\n%s", out)
	}
}

// --warnings is a one-off. It must not write cppm.json, because trying a level
// out should not produce a commit, and the committed level is the one CI uses.
func TestWarningsFlagDoesNotTouchTheManifest(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cmake")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Transient")
	proj := filepath.Join(dir, "Transient")
	before := readFile(t, filepath.Join(proj, "cppm.json"))

	out := mustRun(t, proj, "build", "--warnings=strict")
	if after := readFile(t, filepath.Join(proj, "cppm.json")); after != before {
		t.Errorf("--warnings rewrote cppm.json:\n%s", after)
	}
	// And it has to say so, because the generated CMake now disagrees with
	// cppm.json and the next plain build will put it back.
	if !strings.Contains(out, "not saved to cppm.json") {
		t.Errorf("the override was applied silently:\n%s", out)
	}
}

// Hardening belongs in optimised builds, where it costs almost nothing.
// _FORTIFY_SOURCE in particular is implemented via __builtin_object_size and
// computes nothing until the optimiser runs, so at -O0 it is pure noise.
func TestHardeningAppliesToOptimisedBuildsOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireGNUStyleCompiler(t)

	dir := t.TempDir()
	mustRun(t, dir, "init", "Hardened")
	proj := filepath.Join(dir, "Hardened")
	mustRun(t, proj, "build")
	mustRun(t, proj, "build", "--release")

	debug := compileFlagsFor(t, proj, "debug")["main.cpp"]
	release := compileFlagsFor(t, proj, "release")["main.cpp"]

	if strings.Contains(debug, "_FORTIFY_SOURCE") {
		t.Errorf("_FORTIFY_SOURCE is in the debug build, where it does nothing:\n%s", debug)
	}
	if !strings.Contains(release, "-D_FORTIFY_SOURCE=2") {
		t.Errorf("the release build is not fortified:\n%s", release)
	}
	// The undefine has to come first, or distros that predefine the macro warn
	// on every translation unit.
	if u, d := strings.Index(release, "-U_FORTIFY_SOURCE"), strings.Index(release, "-D_FORTIFY_SOURCE"); u < 0 || u > d {
		t.Errorf("-U_FORTIFY_SOURCE does not precede the define:\n%s", release)
	}

	// The ABI-compatible assertions belong in both.
	for name, cmd := range map[string]string{"debug": debug, "release": release} {
		if !strings.Contains(cmd, "-D_GLIBCXX_ASSERTIONS") {
			t.Errorf("the %s build is missing _GLIBCXX_ASSERTIONS:\n%s", name, cmd)
		}
	}

	// And the compile must not have emitted a redefinition warning, which is
	// what the -U is there to prevent.
	out := mustRun(t, proj, "build", "--release", "--clean")
	if strings.Contains(out, "redefined") {
		t.Errorf("the hardened release build warns about a redefined macro:\n%s", out)
	}
}

// Turning both off has to leave a project that configures and builds with
// nothing added, which is the escape hatch for a codebase that cannot yet
// survive -Wall.
func TestSafetyCanBeTurnedOffEntirely(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireGNUStyleCompiler(t)

	dir := t.TempDir()
	mustRun(t, dir, "init", "Bare")
	proj := filepath.Join(dir, "Bare")

	manifestPath := filepath.Join(proj, "cppm.json")
	m := readFile(t, manifestPath)
	off := strings.Replace(m,
		`"warnings": "default",`+"\n    "+`"hardening": true`,
		`"warnings": "off",`+"\n    "+`"hardening": false`, 1)
	if off == m {
		t.Fatalf("could not switch the safety block off; the manifest layout changed:\n%s", m)
	}
	mustWrite(t, manifestPath, off)

	mustRun(t, proj, "build")
	cmd := compileFlagsFor(t, proj, "debug")["main.cpp"]
	for _, forbidden := range []string{"-Wall", "-Wextra", "_GLIBCXX_ASSERTIONS", "-fstack-protector-strong"} {
		if strings.Contains(cmd, forbidden) {
			t.Errorf("safety off still compiled with %s:\n%s", forbidden, cmd)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
