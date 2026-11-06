// Package e2e drives the real cppm binary against a real CMake and compiler.
//
// The unit tests check that cppm produces the CMake it intends to. These check
// that the CMake actually configures, builds and links -- which is the only
// claim that matters to a user.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var cppmBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cppm-e2e-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	cppmBin = filepath.Join(dir, "cppm")
	build := exec.Command("go", "build", "-o", cppmBin, "cppm/cmd/cppm")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("building cppm for e2e tests: " + err.Error())
	}
	os.Exit(m.Run())
}

// run executes cppm in dir and returns combined output.
func run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(cppmBin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(t, dir, args...)
	if err != nil {
		t.Fatalf("cppm %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not available", name)
	}
}

func TestInitThenBuildProducesAnExecutable(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "HelloApp")
	proj := filepath.Join(dir, "HelloApp")

	for _, f := range []string{
		"cppm.json", "cppm.lock", "CMakeLists.txt", "CMakePresets.json",
		"src/main.cpp", ".gitignore",
		".cppm/generated/cppm_dependencies.cmake",
	} {
		if _, err := os.Stat(filepath.Join(proj, f)); err != nil {
			t.Errorf("init did not create %s", f)
		}
	}

	mustRun(t, proj, "build")

	exe := filepath.Join(proj, "build", "debug", "HelloApp")
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("no executable produced at %s", exe)
	}

	// The binary must actually run and print what the template says.
	out, err := exec.Command(exe).CombinedOutput()
	if err != nil {
		t.Fatalf("running the built binary failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Hello from HelloApp") {
		t.Errorf("unexpected program output: %q", out)
	}
}

// CMAKE_CXX_EXTENSIONS defaults to ON in CMake, which silently compiles the
// user's code as gnu++NN. A generated project must not do that.
func TestGeneratedProjectUsesStrictDialect(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cmake")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "Strict", "--std", "17")
	proj := filepath.Join(dir, "Strict")
	mustRun(t, proj, "build")

	b, err := os.ReadFile(filepath.Join(proj, "build", "debug", "compile_commands.json"))
	if err != nil {
		t.Skipf("no compile_commands.json: %v", err)
	}
	cc := string(b)
	if !strings.Contains(cc, "-std=c++17") {
		t.Errorf("expected -std=c++17 in compile commands")
	}
	if strings.Contains(cc, "gnu++") {
		t.Error("GNU extensions leaked into the build despite CMAKE_CXX_EXTENSIONS OFF")
	}
}

// sync must be idempotent: a second run may not alter the generated file, or
// every build re-configures for no reason.
func TestSyncIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "Idem")
	proj := filepath.Join(dir, "Idem")
	gen := filepath.Join(proj, ".cppm", "generated", "cppm_dependencies.cmake")

	first, err := os.ReadFile(gen)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		mustRun(t, proj, "sync")
	}
	second, err := os.ReadFile(gen)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("sync changed the generated file on a repeat run")
	}
}

// cppm must never rewrite CMakeLists.txt after init: the whole design depends on
// the user being able to edit it freely.
func TestUserEditsToCMakeListsSurviveSync(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "Mine")
	proj := filepath.Join(dir, "Mine")
	cml := filepath.Join(proj, "CMakeLists.txt")

	edited := "# my own comment, added by hand\n" + readFile(t, cml)
	if err := os.WriteFile(cml, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, proj, "sync")

	if got := readFile(t, cml); got != edited {
		t.Error("sync modified the user-owned CMakeLists.txt")
	}
}

// init must not clobber files the user already has.
func TestInitKeepsExistingUserFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Keep", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "Keep", "src", "main.cpp")
	if err := os.WriteFile(main, []byte("int main(){return 42;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := mustRun(t, dir, "init", "Keep")
	if !strings.Contains(out, "kept") {
		t.Errorf("expected init to report keeping an existing file:\n%s", out)
	}
	if got := readFile(t, main); !strings.Contains(got, "42") {
		t.Error("init overwrote an existing source file")
	}
}

func TestInitRefusesToOverwriteManifestWithoutForce(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", ".")
	if _, err := run(t, dir, "init", "."); err == nil {
		t.Error("expected a second init to fail without --force")
	}
	if _, err := run(t, dir, "init", ".", "--force"); err != nil {
		t.Error("--force should permit re-initialising")
	}
}

func TestInitRejectsBadStandard(t *testing.T) {
	dir := t.TempDir()
	out, err := run(t, dir, "init", "X", "--std", "21")
	if err == nil {
		t.Error("expected --std 21 to be rejected")
	}
	if !strings.Contains(out, "invalid --std") {
		t.Errorf("unhelpful error message: %s", out)
	}
}

func TestCommandsOutsideProjectFailClearly(t *testing.T) {
	dir := t.TempDir()
	for _, cmd := range []string{"sync", "list", "build", "clean"} {
		out, err := run(t, dir, cmd)
		if err == nil {
			t.Errorf("%s should fail outside a project", cmd)
		}
		if !strings.Contains(out, "not a cppm project") {
			t.Errorf("%s gave an unclear error: %s", cmd, out)
		}
	}
}

func TestUnknownCommandSuggests(t *testing.T) {
	dir := t.TempDir()
	out, _ := run(t, dir, "buidl")
	if !strings.Contains(out, "did you mean") || !strings.Contains(out, "build") {
		t.Errorf("expected a suggestion for a typo, got: %s", out)
	}
}

func TestListWithNoDependencies(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "Empty")
	out := mustRun(t, filepath.Join(dir, "Empty"), "list")
	if !strings.Contains(out, "no dependencies") {
		t.Errorf("unexpected list output: %s", out)
	}
}

func TestDoctorPassesInFreshProject(t *testing.T) {
	requireTool(t, "cmake")
	dir := t.TempDir()
	mustRun(t, dir, "init", "Doc")
	out, err := run(t, filepath.Join(dir, "Doc"), "doctor")
	if err != nil {
		t.Fatalf("doctor failed in a freshly created project:\n%s", out)
	}
	if !strings.Contains(out, "0 failure(s)") {
		t.Errorf("expected no failures:\n%s", out)
	}
}

func TestCleanRemovesBuildButNotSources(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cmake")
	}
	requireTool(t, "cmake")
	dir := t.TempDir()
	mustRun(t, dir, "init", "Cl")
	proj := filepath.Join(dir, "Cl")
	mustRun(t, proj, "build")
	mustRun(t, proj, "clean")

	if _, err := os.Stat(filepath.Join(proj, "build")); !os.IsNotExist(err) {
		t.Error("clean did not remove build/")
	}
	for _, keep := range []string{"cppm.json", "cppm.lock", "CMakeLists.txt", "src/main.cpp"} {
		if _, err := os.Stat(filepath.Join(proj, keep)); err != nil {
			t.Errorf("clean removed %s", keep)
		}
	}
}

// A project in a subdirectory must be found by walking up, like git does.
func TestCommandsWorkFromSubdirectory(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "Deep")
	sub := filepath.Join(dir, "Deep", "src")
	out := mustRun(t, sub, "list")
	if !strings.Contains(out, "no dependencies") {
		t.Errorf("list did not find the project from a subdirectory: %s", out)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Go's flag package stops parsing at the first positional argument, so
// `cppm init MyApp --std 17` would silently ignore --std. cppm permutes
// arguments to avoid that, because it is how people actually type.
func TestFlagsWorkBeforeAndAfterPositionalArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"flags after positional", []string{"init", "A", "--std", "17"}},
		{"flags before positional", []string{"init", "--std", "17", "A"}},
		{"equals form after", []string{"init", "A", "--std=17"}},
		{"single dash after", []string{"init", "A", "-std", "17"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mustRun(t, dir, tc.args...)
			got := readFile(t, filepath.Join(dir, "A", "cppm.json"))
			if !strings.Contains(got, `"standard": 17`) {
				t.Errorf("--std 17 was ignored; manifest says:\n%s", got)
			}
		})
	}
}

// A boolean flag must not swallow the positional argument that follows it.
func TestBooleanFlagDoesNotConsumeNextArg(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "--lib", "MyLib")
	cml := readFile(t, filepath.Join(dir, "MyLib", "CMakeLists.txt"))
	if !strings.Contains(cml, "add_library(MyLib") {
		t.Errorf("--lib did not produce a library target:\n%s", cml)
	}
}
