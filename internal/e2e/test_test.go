package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The claim `cppm init --with-tests` makes is that the scaffolded suite builds
// and runs with no dependency and no network, which is the only way init can
// work offline.
func TestInitWithTestsProducesAPassingSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "ctest")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Tested", "--with-tests")
	proj := filepath.Join(dir, "Tested")

	if _, err := os.Stat(filepath.Join(proj, "tests", "Tested_test.cpp")); err != nil {
		t.Fatalf("--with-tests did not scaffold a test file: %v", err)
	}

	out := mustRun(t, proj, "test")
	if !strings.Contains(out, "100% tests passed") {
		t.Fatalf("the scaffolded suite should pass:\n%s", out)
	}
	if !strings.Contains(out, "Tested_tests") {
		t.Errorf("the test target was not run:\n%s", out)
	}
}

// A test suite whose failures do not fail the command is worse than none: CI
// would go green while the code is broken.
func TestFailingTestFailsTheCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "ctest")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Tested", "--with-tests")
	proj := filepath.Join(dir, "Tested")

	src := filepath.Join(proj, "tests", "Tested_test.cpp")
	body := readFile(t, src)
	broken := strings.Replace(body, "CHECK(1 + 1 == 2);", "CHECK(1 + 1 == 3);", 1)
	if broken == body {
		t.Fatal("could not break the scaffolded test; its template changed")
	}
	if err := os.WriteFile(src, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, proj, "test")
	if err == nil {
		t.Fatalf("a failing test must make 'cppm test' exit non-zero:\n%s", out)
	}
	// outputOnFailure in the test preset is what makes the cause visible;
	// without it the user learns only that something broke.
	if !strings.Contains(out, "FAILED: 1 + 1 == 3") {
		t.Errorf("the failing check's own output should be shown:\n%s", out)
	}
}

// ctest exits 8 with "No tests were found" for a project with none, which reads
// as a build failure. The error has to say what to do instead.
func TestTestWithoutTestsExplainsWhatToDo(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "ctest")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Plain")
	proj := filepath.Join(dir, "Plain")

	out, err := run(t, proj, "test")
	if err == nil {
		t.Fatalf("a project with no tests should not report success:\n%s", out)
	}
	for _, want := range []string{"no tests", "--with-tests", "add_test("} {
		if !strings.Contains(out, want) {
			t.Errorf("the error should mention %q:\n%s", want, out)
		}
	}
}

func TestTestFilterAndList(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "ctest")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Tested", "--with-tests")
	proj := filepath.Join(dir, "Tested")

	list := mustRun(t, proj, "test", "--list")
	if !strings.Contains(list, "Tested_tests") || strings.Contains(list, "100% tests passed") {
		t.Errorf("--list should name the tests without running them:\n%s", list)
	}

	if out := mustRun(t, proj, "test", "--filter", "Tested"); !strings.Contains(out, "100% tests passed") {
		t.Errorf("a matching filter should run the test:\n%s", out)
	}

	// A filter that matches nothing is a user mistake, not an empty success.
	out, err := run(t, proj, "test", "--filter", "nothing-matches-this")
	if err == nil {
		t.Errorf("a filter matching no test should fail:\n%s", out)
	} else if !strings.Contains(out, "--list") {
		t.Errorf("the error should point at --list:\n%s", out)
	}
}

// A filter reaches ctest's argv directly, so one beginning with '-' would be
// read as an option there. cppm rejects rather than escapes, as it does for
// every other argument passed to a tool.
func TestTestRejectsFilterThatLooksLikeAFlag(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "Tested", "--with-tests")
	proj := filepath.Join(dir, "Tested")

	out, err := run(t, proj, "test", "--filter=--output-on-failure")
	if err == nil {
		t.Fatalf("a filter beginning with '-' must be rejected:\n%s", out)
	}
	if !strings.Contains(out, "must not begin with '-'") {
		t.Errorf("unexpected error:\n%s", out)
	}
}

// The scaffolded test target has to be in cppm.json as well as CMakeLists.txt,
// because that declaration is what routes dev dependencies to it.
func TestWithTestsDeclaresTheTargetInTheManifest(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "Tested", "--with-tests")
	proj := filepath.Join(dir, "Tested")

	var m struct {
		Targets map[string]struct {
			Kind string `json:"kind"`
			Test bool   `json:"test"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(proj, "cppm.json"))), &m); err != nil {
		t.Fatal(err)
	}
	tgt, ok := m.Targets["Tested_tests"]
	if !ok {
		t.Fatalf("cppm.json has no test target: %v", m.Targets)
	}
	if !tgt.Test {
		t.Error("the scaffolded test target is not marked \"test\": true, so dev dependencies will not reach it")
	}

	cml := readFile(t, filepath.Join(proj, "CMakeLists.txt"))
	if !strings.Contains(cml, "add_test(NAME Tested_tests COMMAND Tested_tests)") {
		t.Errorf("CMakeLists.txt does not register the test with CTest:\n%s", cml)
	}
}

// Without a test target, a dev dependency is fetched and then linked to
// nothing. Saying so is the difference between a surprise at link time and a
// known next step.
func TestAddDevWithoutATestTargetSaysSo(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "Plain")
	proj := filepath.Join(dir, "Plain")

	// A path dependency needs no network, so this covers the reporting path
	// without reaching GitHub.
	lib := filepath.Join(dir, "SomeLib")
	if err := os.MkdirAll(filepath.Join(lib, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(lib, "CMakeLists.txt"),
		"cmake_minimum_required(VERSION 3.24)\nproject(SomeLib LANGUAGES CXX)\nadd_library(SomeLib src/a.cpp)\n")
	writeFile(t, filepath.Join(lib, "src", "a.cpp"), "int a() { return 1; }\n")

	out := mustRun(t, proj, "add", "local:../SomeLib", "--dev")
	if !strings.Contains(out, "linked to: nothing yet") {
		t.Errorf("a dev dependency with no test target should say it is linked to nothing:\n%s", out)
	}
	if !strings.Contains(out, "--with-tests") {
		t.Errorf("the note should say how to get a test target:\n%s", out)
	}
}

// The payoff of 0.2.2: a dev dependency must not reach a shipping target.
func TestDevDependencyIsKeptOutOfTheShippingTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "App", "--with-tests")
	proj := filepath.Join(dir, "App")

	// A local library standing in for a test framework: no network, and its
	// symbol is easy to look for.
	lib := filepath.Join(dir, "FakeFramework")
	if err := os.MkdirAll(filepath.Join(lib, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(lib, "CMakeLists.txt"),
		"cmake_minimum_required(VERSION 3.24)\nproject(FakeFramework LANGUAGES CXX)\n"+
			"add_library(FakeFramework src/f.cpp)\n"+
			"target_include_directories(FakeFramework PUBLIC include)\n")
	writeFile(t, filepath.Join(lib, "src", "f.cpp"), "int cppm_fake_framework_marker() { return 7; }\n")

	mustRun(t, proj, "add", "local:../FakeFramework", "--dev", "--target", "FakeFramework")

	generated := readFile(t, filepath.Join(proj, ".cppm", "generated", "cppm.cmake"))
	if strings.Contains(generated, "list(APPEND CPPM_DEPENDENCIES FakeFramework)") {
		t.Error("a dev dependency must not be in CPPM_DEPENDENCIES, which is what shipping targets link")
	}

	// Call the dev dependency's symbol from the test, and not from the app. If
	// cppm wires this correctly the test links and the app still builds.
	writeFile(t, filepath.Join(proj, "tests", "App_test.cpp"),
		"int cppm_fake_framework_marker();\nint main() { return cppm_fake_framework_marker() == 7 ? 0 : 1; }\n")

	out := mustRun(t, proj, "test")
	if !strings.Contains(out, "100% tests passed") {
		t.Fatalf("the test target should have the dev dependency linked:\n%s", out)
	}

	// And the app must have built without it. Nothing in CMakeLists.txt links
	// FakeFramework to App, so a link would mean CPPM_DEPENDENCIES leaked.
	if _, err := os.Stat(filepath.Join(proj, "build", "debug", exeName("App"))); err != nil {
		t.Errorf("the shipping target should still build: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
