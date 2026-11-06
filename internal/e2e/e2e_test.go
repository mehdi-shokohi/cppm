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
		".cppm/generated/cppm.cmake",
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
	gen := filepath.Join(proj, ".cppm", "generated", "cppm.cmake")

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

// The point of putting the standard in the generated include rather than in
// CMakeLists.txt: editing cppm.json and running sync must reach the build.
func TestStandardChangeInManifestReachesTheBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "App", "--std", "17")
	proj := filepath.Join(dir, "App")
	mustRun(t, proj, "build")

	if got := compileFlags(t, proj); !strings.Contains(got, "-std=c++17") {
		t.Fatalf("expected c++17 initially, got: %s", got)
	}

	// Edit the manifest the way a user would.
	manifestPath := filepath.Join(proj, "cppm.json")
	edited := strings.Replace(readFile(t, manifestPath), `"standard": 17`, `"standard": 20`, 1)
	if err := os.WriteFile(manifestPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	out := mustRun(t, proj, "sync")
	if !strings.Contains(out, "17 -> 20") {
		t.Errorf("sync should report the standard change, got: %s", out)
	}
	mustRun(t, proj, "build")

	got := compileFlags(t, proj)
	if !strings.Contains(got, "-std=c++20") {
		t.Errorf("the standard change did not reach the build: %s", got)
	}
	if strings.Contains(got, "c++17") {
		t.Error("the old standard is still in use")
	}
}

// "apply" and "regen" are aliases, because that is what people reach for.
func TestSyncAliases(t *testing.T) {
	for _, alias := range []string{"sync", "apply", "regen"} {
		t.Run(alias, func(t *testing.T) {
			dir := t.TempDir()
			mustRun(t, dir, "init", "A")
			out := mustRun(t, filepath.Join(dir, "A"), alias)
			if !strings.Contains(out, "up to date") {
				t.Errorf("%s did not behave like sync: %s", alias, out)
			}
		})
	}
}

// The compiler cannot live in the generated include, because CMAKE_CXX_COMPILER
// must be set before project() runs. It goes into the presets instead.
func TestCompilerSelectionReachesTheBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "clang++")

	dir := t.TempDir()
	mustRun(t, dir, "init", "App", "--compiler", "clang++")
	proj := filepath.Join(dir, "App")

	if !strings.Contains(readFile(t, filepath.Join(proj, "CMakePresets.json")), "clang++") {
		t.Error("the compiler should be written into CMakePresets.json")
	}
	mustRun(t, proj, "build")
	if got := compileFlags(t, proj); !strings.Contains(got, "clang") {
		t.Errorf("clang++ was requested but did not compile the project: %s", got)
	}
}

// CMake refuses to change compiler in an existing build tree. cppm detects that
// and starts the tree afresh, because build output is disposable.
func TestChangingCompilerRebuildsCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")
	requireTool(t, "clang++")
	requireTool(t, "g++")

	dir := t.TempDir()
	mustRun(t, dir, "init", "App", "--compiler", "g++")
	proj := filepath.Join(dir, "App")
	mustRun(t, proj, "build")

	manifestPath := filepath.Join(proj, "cppm.json")
	edited := strings.Replace(readFile(t, manifestPath), `"cxx": "g++"`, `"cxx": "clang++"`, 1)
	if err := os.WriteFile(manifestPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, proj, "sync")

	out := mustRun(t, proj, "build")
	if !strings.Contains(out, "compiler changed") {
		t.Errorf("expected cppm to notice the compiler change, got: %s", out)
	}
	if got := compileFlags(t, proj); !strings.Contains(got, "clang") {
		t.Errorf("build still uses the old compiler: %s", got)
	}
}

// "clang" compiles C++ but does not link the standard library, which produces a
// wall of undefined references. cppm corrects it rather than letting that happen.
func TestCDriverIsCorrectedToCxxDriver(t *testing.T) {
	dir := t.TempDir()
	out := mustRun(t, dir, "init", "A", "--compiler", "clang")
	if !strings.Contains(out, "clang++") {
		t.Errorf("expected clang to be corrected to clang++: %s", out)
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "A", "cppm.json")), `"clang++"`) {
		t.Error("manifest should record the C++ driver, not the C driver")
	}
}

// sync may rewrite presets, so it must not silently discard a hand-written file.
func TestSyncRefusesToOverwriteForeignPresets(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "A")
	proj := filepath.Join(dir, "A")
	presets := filepath.Join(proj, "CMakePresets.json")

	mine := `{"version":3,"configurePresets":[{"name":"mine","binaryDir":"b"}]}`
	if err := os.WriteFile(presets, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force a regeneration attempt by changing something presets depend on.
	manifestPath := filepath.Join(proj, "cppm.json")
	edited := strings.Replace(readFile(t, manifestPath), `"minimum": "3.24"`, `"minimum": "3.25"`, 1)
	if err := os.WriteFile(manifestPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	out := mustRun(t, proj, "sync")
	if !strings.Contains(out, "no cppm vendor stamp") {
		t.Errorf("expected sync to report skipping foreign presets, got: %s", out)
	}
	if readFile(t, presets) != mine {
		t.Error("sync overwrote a hand-written CMakePresets.json")
	}
}

// doctor should catch an unsupported standard before CMake fails with an error
// that never mentions cppm.json.
func TestDoctorDetectsUnsupportedStandard(t *testing.T) {
	requireTool(t, "cmake")
	dir := t.TempDir()
	mustRun(t, dir, "init", "A")
	proj := filepath.Join(dir, "A")

	manifestPath := filepath.Join(proj, "cppm.json")
	edited := strings.Replace(readFile(t, manifestPath), `"standard": 20`, `"standard": 26`, 1)
	if err := os.WriteFile(manifestPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, proj, "sync")

	out, _ := run(t, proj, "doctor")
	// Either the compiler supports C++26 and this passes, or doctor says so.
	if strings.Contains(out, "does not support C++26") {
		return
	}
	if !strings.Contains(out, "C++26, supported by") {
		t.Errorf("doctor said nothing useful about the standard:\n%s", out)
	}
}

// doctor must flag a standard set after the include, which would silently
// override cppm.json and make sync appear to do nothing.
func TestDoctorDetectsStandardShadowing(t *testing.T) {
	requireTool(t, "cmake")
	dir := t.TempDir()
	mustRun(t, dir, "init", "A")
	proj := filepath.Join(dir, "A")

	cml := filepath.Join(proj, "CMakeLists.txt")
	shadowed := readFile(t, cml) + "\nset(CMAKE_CXX_STANDARD 11)\n"
	if err := os.WriteFile(cml, []byte(shadowed), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := run(t, proj, "doctor")
	if !strings.Contains(out, "after the cppm include") {
		t.Errorf("doctor did not flag a shadowed standard:\n%s", out)
	}
}

// compileFlags returns the debug preset's compile_commands.json.
func compileFlags(t *testing.T, proj string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(proj, "build", "debug", "compile_commands.json"))
	if err != nil {
		t.Skipf("no compile_commands.json: %v", err)
	}
	return string(b)
}

// Defaults are written explicitly so the user can see and edit them. An unset
// field teaches nobody anything.
func TestInitWritesExplicitDefaults(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "A")
	m := readFile(t, filepath.Join(dir, "A", "cppm.json"))
	for _, want := range []string{
		`"cxx"`, `"standard": 20`, `"extensions": false`,
		`"minimum"`, `"exportCompileCommands": true`, `"buildTypes"`,
		`"toolchain"`, `"generator"`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("cppm.json does not spell out %s:\n%s", want, m)
		}
	}
	// A concrete compiler, not the "c++" symlink, so the reader knows which
	// compiler they are getting.
	if !strings.Contains(m, `"cxx": "g++"`) && !strings.Contains(m, `"cxx": "clang++"`) {
		t.Errorf("expected a concrete compiler name in the manifest:\n%s", m)
	}
}

func TestBuildTypeIsConfigurable(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cmake")
	}
	requireTool(t, "cmake")
	dir := t.TempDir()
	mustRun(t, dir, "init", "A")
	proj := filepath.Join(dir, "A")

	mPath := filepath.Join(proj, "cppm.json")
	edited := strings.Replace(readFile(t, mPath), `"release": "RelWithDebInfo"`, `"release": "Release"`, 1)
	if err := os.WriteFile(mPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, proj, "sync")

	presets := readFile(t, filepath.Join(proj, "CMakePresets.json"))
	if !strings.Contains(presets, `"Release"`) {
		t.Errorf("the release build type did not reach the presets:\n%s", presets)
	}
}

func TestCompileCommandsCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "A")
	proj := filepath.Join(dir, "A")

	mPath := filepath.Join(proj, "cppm.json")
	edited := strings.Replace(readFile(t, mPath), `"exportCompileCommands": true`, `"exportCompileCommands": false`, 1)
	if err := os.WriteFile(mPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, proj, "sync")

	if strings.Contains(readFile(t, filepath.Join(proj, "CMakePresets.json")), "EXPORT_COMPILE_COMMANDS") {
		t.Error("compile_commands.json export should be absent when disabled")
	}
}

// Declaring C is what stops CMake warning that CMAKE_C_COMPILER went unused,
// and dependencies routinely contain C sources.
func TestProjectEnablesCWhenACompilerIsConfigured(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "A")
	cml := readFile(t, filepath.Join(dir, "A", "CMakeLists.txt"))
	if !strings.Contains(cml, "LANGUAGES CXX C") {
		t.Errorf("expected C to be enabled alongside CXX:\n%s", cml)
	}
}
