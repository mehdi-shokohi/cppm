package cmakegen

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mehdi-shokohi/cppm/internal/lockfile"
	"github.com/mehdi-shokohi/cppm/internal/manifest"
	"github.com/mehdi-shokohi/cppm/internal/safety"
)

func sampleLock() *lockfile.Lock {
	l := lockfile.New("test")
	l.Put(lockfile.Package{
		Name:         "fmt",
		Source:       manifest.SourceGit,
		URL:          "https://github.com/fmtlib/fmt.git",
		Requested:    "11.1.4",
		Commit:       "123913715afeb8a437e6388b4473fcc4753e1c9a",
		Targets:      []string{"fmt::fmt"},
		TargetOrigin: lockfile.OriginInferred,
		Options:      map[string]string{"FMT_TEST": "OFF", "FMT_DOC": "OFF"},
	})
	return l
}

// The generated file's public names live inside a user-owned CMakeLists.txt that
// cppm promised never to rewrite, so cppm cannot migrate them later. Changing
// any of these breaks every existing project: that is what this test guards.
func TestPublicInterfaceIsStable(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	for _, name := range []string{
		"function(cppm_link_dependencies",
		"CPPM_TARGETS_fmt",
		"set(CPPM_DEPENDENCIES",
	} {
		if !strings.Contains(out, name) {
			t.Errorf("public interface %q is missing from the generated file", name)
		}
	}
}

// Everything that is not public must be visibly private, so users do not come
// to depend on it.
func TestPrivateNamesArePrefixed(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "set(") {
			continue
		}
		name := strings.TrimPrefix(line, "set(")
		if i := strings.IndexAny(name, " )"); i > 0 {
			name = name[:i]
		}
		switch {
		case strings.HasPrefix(name, "CPPM_"), strings.HasPrefix(name, "_cppm_"):
			// public, or explicitly private
		case strings.HasPrefix(name, "${_cppm_"):
			// set() through a function's out-parameter, e.g.
			// set(${_cppm_out} ... PARENT_SCOPE). The name is still private;
			// it is just chosen by the caller.
		case strings.HasPrefix(name, "CMAKE_"), strings.HasPrefix(name, "MEMORYCHECK_"):
			// A CMake-owned or CTest-owned variable, including the computed
			// CMAKE_${_cppm_lf}_LINKER_FLAGS form. These are not cppm's names
			// to prefix -- they are the ones CMake reads.
		case name == strings.ToUpper(name):
			// a dependency's own cache option, e.g. FMT_TEST
		default:
			t.Errorf("variable %q is neither public (CPPM_) nor marked private (_cppm_)", name)
		}
	}
}

// Cache options only take effect before the dependency is made available.
// Emitting them afterwards silently does nothing, and every dependency then
// builds its own tests and examples.
func TestOptionsArePlacedBeforeDeclare(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	optIdx := strings.Index(out, "FMT_TEST")
	declIdx := strings.Index(out, "FetchContent_Declare(fmt")
	availIdx := strings.Index(out, "FetchContent_MakeAvailable")
	if optIdx < 0 || declIdx < 0 || availIdx < 0 {
		t.Fatalf("expected option, Declare and MakeAvailable in output")
	}
	if optIdx > declIdx {
		t.Error("cache options must be emitted before FetchContent_Declare")
	}
	if declIdx > availIdx {
		t.Error("FetchContent_Declare must precede FetchContent_MakeAvailable")
	}
}

func TestVersionGatedKeywords(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	// SYSTEM is 3.25+, EXCLUDE_FROM_ALL is 3.28+, but the generated floor is
	// 3.24, so both must be behind a version check rather than emitted directly.
	if !strings.Contains(out, "VERSION_GREATER_EQUAL 3.25") {
		t.Error("SYSTEM must be gated on CMake 3.25")
	}
	if !strings.Contains(out, "VERSION_GREATER_EQUAL 3.28") {
		t.Error("EXCLUDE_FROM_ALL must be gated on CMake 3.28")
	}
}

func TestDeterministicOutput(t *testing.T) {
	first := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	for i := 0; i < 10; i++ {
		if again := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil); again != first {
			t.Fatalf("output differs between runs on iteration %d", i)
		}
	}
}

func TestBannerPresent(t *testing.T) {
	if !strings.Contains(Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil), "DO NOT EDIT") {
		t.Error("generated file must carry a do-not-edit banner")
	}
}

func TestEmptyLockStillProducesUsableFile(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), lockfile.New("test"), "test", nil)
	// A project with no dependencies still includes this file, so the linking
	// function must exist or every fresh project fails to configure.
	if !strings.Contains(out, "function(cppm_link_dependencies") {
		t.Error("cppm_link_dependencies must be defined even with no dependencies")
	}
	if strings.Contains(out, "FetchContent_MakeAvailable") {
		t.Error("no MakeAvailable call should be emitted when there is nothing to fetch")
	}
}

// Values come from a manifest and are not trusted to be free of CMake
// metacharacters. The assertions target the specific emitted lines, because the
// generated file legitimately contains ${_cppm_dep} references of its own.
func TestQuotingEscapesMetacharacters(t *testing.T) {
	l := lockfile.New("test")
	l.Put(lockfile.Package{
		Name:    "evil",
		Source:  manifest.SourceGit,
		URL:     `https://example.invalid/a".git`,
		Commit:  "0000000000000000000000000000000000000000",
		Options: map[string]string{"X": `a"b${Y}\c`},
	})
	out := Managed(manifest.New("MyApp", 20), l, "test", nil)

	optLine := findLine(t, out, "set(X ")
	if !strings.Contains(optLine, `\"`) {
		t.Errorf("quote not escaped in option line: %s", optLine)
	}
	if !strings.Contains(optLine, `\${Y}`) {
		t.Errorf("variable reference not escaped in option line: %s", optLine)
	}
	if strings.Contains(optLine, `\\${Y}`) {
		t.Errorf("variable reference double-escaped: %s", optLine)
	}

	urlLine := findLine(t, out, "GIT_REPOSITORY")
	if !strings.Contains(urlLine, `a\".git`) {
		t.Errorf("quote not escaped in url line: %s", urlLine)
	}
}

// findLine returns the single line containing substr, failing if absent.
func findLine(t *testing.T, out, substr string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, substr) {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("no line containing %q in output", substr)
	return ""
}

// The standard lives in the generated include, not in CMakeLists.txt, so that
// editing cppm.json and running sync actually changes the build.
func TestManagedSetsLanguageSettings(t *testing.T) {
	m := manifest.New("MyApp", 26)
	out := Managed(m, lockfile.New("t"), "t", nil)
	if !strings.Contains(out, "set(CMAKE_CXX_STANDARD 26)") {
		t.Error("standard must be written into the generated include")
	}
	if !strings.Contains(out, "set(CMAKE_CXX_EXTENSIONS OFF)") {
		t.Error("CMAKE_CXX_EXTENSIONS OFF is required: it defaults to ON and silently selects gnu++NN")
	}
	// Dependencies must inherit the standard, so it has to be set first.
	stdIdx := strings.Index(out, "set(CMAKE_CXX_STANDARD")
	depIdx := strings.Index(out, "FetchContent_Declare")
	if stdIdx < 0 {
		t.Fatal("no standard in output")
	}
	if depIdx >= 0 && stdIdx > depIdx {
		t.Error("the standard must be set before dependencies are declared")
	}
}

func TestCMakeListsDelegatesToGeneratedInclude(t *testing.T) {
	m := manifest.New("MyApp", 20)
	out := CMakeLists(m)
	if strings.Contains(out, "CMAKE_CXX_STANDARD") {
		t.Error("CMakeLists.txt must not set the standard: it would shadow cppm.json and sync could never change it")
	}
	incIdx := strings.Index(out, GeneratedRelPath)
	tgtIdx := strings.Index(out, "add_executable")
	if incIdx < 0 {
		t.Fatal("CMakeLists.txt does not include the generated file")
	}
	if tgtIdx < 0 || incIdx > tgtIdx {
		t.Error("the generated include must come before the targets")
	}
}

// The compiler cannot come from the generated include, because
// CMAKE_CXX_COMPILER must be set before project() runs.
func TestCompilerGoesIntoPresetsNotTheInclude(t *testing.T) {
	m := manifest.New("MyApp", 20)
	m.Toolchain.CXX = "clang++"
	m.Toolchain.C = "clang"

	if strings.Contains(Managed(m, lockfile.New("t"), "t", nil), "CMAKE_CXX_COMPILER") {
		t.Error("the compiler must not be set in the generated include; it runs after project()")
	}
	out, err := Presets(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CMAKE_CXX_COMPILER", "clang++", "CMAKE_C_COMPILER", "clang"} {
		if !strings.Contains(out, want) {
			t.Errorf("presets missing %s", want)
		}
	}
}

// sync may rewrite presets, so it must be able to tell its own output from a
// hand-written file.
func TestPresetsVendorStampRoundTrips(t *testing.T) {
	m := manifest.New("MyApp", 20)
	out, err := Presets(m)
	if err != nil {
		t.Fatal(err)
	}
	if !PresetsAreCppmGenerated([]byte(out)) {
		t.Error("cppm-generated presets must carry the vendor stamp")
	}
	for _, foreign := range []string{
		`{"version":3,"configurePresets":[]}`,
		`{"version":3,"vendor":{"someoneelse":{}},"configurePresets":[]}`,
		`not json at all`,
	} {
		if PresetsAreCppmGenerated([]byte(foreign)) {
			t.Errorf("a file without the stamp must not be claimed as cppm-generated: %s", foreign)
		}
	}
}

func TestCMakeListsLibraryKind(t *testing.T) {
	m := manifest.New("MyLib", 20)
	m.Targets["MyLib"] = manifest.Target{Kind: manifest.KindLibrary, Sources: []string{"src/a.cpp"}}
	if !strings.Contains(CMakeLists(m), "add_library(MyLib") {
		t.Error("library kind should produce add_library")
	}
}

func TestPresetsAreValidJSON(t *testing.T) {
	m := manifest.New("MyApp", 20)
	m.Toolchain.Generator = "Ninja"
	out, err := Presets(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"version": 3`, `"debug"`, `"release"`, `"Ninja"`, `"configurePresets"`, `"buildPresets"`} {
		if !strings.Contains(out, want) {
			t.Errorf("presets missing %s", want)
		}
	}
}

func TestPathDependencyUsesAddSubdirectory(t *testing.T) {
	l := lockfile.New("test")
	l.Put(lockfile.Package{
		Name:    "MyLib",
		Source:  manifest.SourcePath,
		Path:    "../MyLib",
		Targets: []string{"MyLib"},
	})
	out := Managed(manifest.New("MyApp", 20), l, "test", nil)
	if !strings.Contains(out, "add_subdirectory") {
		t.Error("path dependencies should use add_subdirectory, not FetchContent")
	}
	if strings.Contains(out, "FetchContent_Declare(MyLib") {
		t.Error("path dependency must not be fetched")
	}
}

// --- dev dependencies and testing (roadmap 0.2.1, 0.2.2) ---

// devLock is sampleLock plus a test-only dependency.
func devLock() *lockfile.Lock {
	l := sampleLock()
	l.Put(lockfile.Package{
		Name:         "Catch2",
		Source:       manifest.SourceGit,
		URL:          "https://github.com/catchorg/Catch2.git",
		Requested:    "v3.7.1",
		Commit:       "fa43b77429ba76c462b1898d6cd2f2d7a9416b14",
		Targets:      []string{"Catch2::Catch2WithMain"},
		TargetOrigin: lockfile.OriginRecipe,
		Dev:          true,
	})
	return l
}

// withTests returns a manifest holding one executable and one test target.
func withTests() *manifest.Manifest {
	m := manifest.New("MyApp", 20)
	m.Targets["MyApp_tests"] = manifest.Target{
		Kind:    manifest.KindExecutable,
		Sources: []string{"tests/MyApp_test.cpp"},
		Test:    true,
	}
	return m
}

// The entire point of the dev flag: CPPM_DEPENDENCIES is what
// cppm_link_dependencies() links by default, so a dev dependency appearing in it
// would compile a test framework into a release binary.
func TestDevDependencyIsNotInCppmDependencies(t *testing.T) {
	out := Managed(withTests(), devLock(), "test", nil)

	if !strings.Contains(out, "list(APPEND CPPM_DEPENDENCIES fmt)") {
		t.Error("a normal dependency must be appended to CPPM_DEPENDENCIES")
	}
	if strings.Contains(out, "list(APPEND CPPM_DEPENDENCIES Catch2)") {
		t.Error("a dev dependency must NOT be appended to CPPM_DEPENDENCIES")
	}
	if !strings.Contains(out, "list(APPEND _cppm_dev_dependencies Catch2)") {
		t.Error("a dev dependency must be tracked in _cppm_dev_dependencies")
	}
	// It still has to be fetched and verified, or CPPM_TARGETS_Catch2 is a lie.
	if !strings.Contains(out, "FetchContent_MakeAvailable(Catch2 fmt)") {
		t.Error("a dev dependency must still be made available")
	}
	if !strings.Contains(out, "list(APPEND _cppm_all_dependencies Catch2)") {
		t.Error("a dev dependency must be verified like any other")
	}
}

// A dev dependency reaches the test target through the generated function, not
// through an edit to CMakeLists.txt, because cppm never rewrites that file.
func TestTestTargetsReceiveDevDependencies(t *testing.T) {
	out := Managed(withTests(), devLock(), "test", nil)
	if !strings.Contains(out, "set(_cppm_test_targets MyApp_tests)") {
		t.Fatalf("test targets were not declared to the generated include:\n%s", out)
	}
	if !strings.Contains(out, `if("${_cppm_link_target}" IN_LIST _cppm_test_targets)`) {
		t.Error("cppm_link_dependencies does not add dev dependencies for test targets")
	}
}

// A project with no test target must still produce a valid file: the variable
// has to exist, because the function dereferences it unconditionally.
func TestTestTargetListIsAlwaysDefined(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	if !strings.Contains(out, `set(_cppm_test_targets "")`) {
		t.Error("_cppm_test_targets must be defined even with no test targets")
	}
}

// include(CTest) defines BUILD_TESTING and defaults it to ON, and a great many
// libraries build their own tests when it is set. Emitting it before the
// dependencies would add every dependency's test suite to the build -- the same
// failure mode as emitting cache options after FetchContent_Declare.
func TestCTestComesAfterTheDependencies(t *testing.T) {
	out := Managed(withTests(), devLock(), "test", nil)

	ctest := strings.Index(out, "include(CTest)")
	available := strings.Index(out, "FetchContent_MakeAvailable(")
	declare := strings.Index(out, "FetchContent_Declare(")
	if ctest < 0 {
		t.Fatal("include(CTest) is missing, so add_test() registers nothing")
	}
	if ctest < available {
		t.Error("include(CTest) must come after FetchContent_MakeAvailable, or dependencies build their own tests")
	}

	// BUILD_TESTING is forced off for the dependency section for the same
	// reason, and restored for the project's own tests.
	off := strings.Index(out, "set(BUILD_TESTING OFF)")
	restore := strings.Index(out, `set(BUILD_TESTING "${_cppm_saved_build_testing}")`)
	if off < 0 || restore < 0 {
		t.Fatalf("BUILD_TESTING is not suppressed around the dependency section:\n%s", out)
	}
	if off > declare {
		t.Error("BUILD_TESTING must be forced off before FetchContent_Declare, where it is still inert")
	}
	if restore < available {
		t.Error("BUILD_TESTING must be restored only after the dependencies are configured")
	}
}

// A cppm project consumed through add_subdirectory must not switch testing on
// in its parent.
func TestCTestIsGuardedToTheTopLevelProject(t *testing.T) {
	out := Managed(withTests(), devLock(), "test", nil)
	idx := strings.Index(out, "include(CTest)")
	if idx < 0 {
		t.Fatal("include(CTest) is missing")
	}
	if !strings.Contains(out[:idx], "if(PROJECT_IS_TOP_LEVEL)") {
		t.Error("include(CTest) must be guarded by PROJECT_IS_TOP_LEVEL")
	}
}

func TestCMakeListsRegistersTestTargetsWithCTest(t *testing.T) {
	out := CMakeLists(withTests())
	if !strings.Contains(out, "add_test(NAME MyApp_tests COMMAND MyApp_tests)") {
		t.Errorf("a test target was not registered with CTest:\n%s", out)
	}
	if strings.Contains(out, "add_test(NAME MyApp COMMAND MyApp)") {
		t.Error("a non-test target must not be registered with CTest")
	}
}

// target_link_libraries() treats a name that is not yet a target as a plain -l
// flag, so a test target that links the library must be declared after it.
func TestTestTargetsAreEmittedAfterTheTargetsTheyLink(t *testing.T) {
	m := manifest.New("MyLib", 20)
	m.Targets["MyLib"] = manifest.Target{Kind: manifest.KindLibrary, Sources: []string{"src/MyLib.cpp"}}
	// Named so that it sorts BEFORE the library, which is what makes this a
	// real risk rather than an accident of alphabetical order.
	m.Targets["AAA_tests"] = manifest.Target{
		Kind: manifest.KindExecutable, Sources: []string{"tests/t.cpp"}, Test: true,
	}
	out := CMakeLists(m)

	lib := strings.Index(out, "add_library(MyLib")
	test := strings.Index(out, "add_executable(AAA_tests")
	if lib < 0 || test < 0 {
		t.Fatalf("both targets should be emitted:\n%s", out)
	}
	if test < lib {
		t.Error("the test target must be emitted after the library it links")
	}
	if !strings.Contains(out, "target_link_libraries(AAA_tests PRIVATE MyLib)") {
		t.Errorf("a test target in a library project should link that library:\n%s", out)
	}
}

// With two libraries cppm cannot know which the tests are for, and a wrongly
// linked scaffold is worse than a comment saying what to write.
func TestAmbiguousLibrariesAreNotGuessed(t *testing.T) {
	m := manifest.New("MyApp", 20)
	m.Targets["LibA"] = manifest.Target{Kind: manifest.KindLibrary, Sources: []string{"a.cpp"}}
	m.Targets["LibB"] = manifest.Target{Kind: manifest.KindLibrary, Sources: []string{"b.cpp"}}
	m.Targets["t"] = manifest.Target{Kind: manifest.KindExecutable, Sources: []string{"t.cpp"}, Test: true}
	out := CMakeLists(m)
	if strings.Contains(out, "target_link_libraries(t PRIVATE Lib") {
		t.Errorf("cppm must not pick between two libraries:\n%s", out)
	}
}

func TestPresetsCarryTestPresets(t *testing.T) {
	out, err := Presets(manifest.New("MyApp", 20))
	if err != nil {
		t.Fatal(err)
	}
	var pf struct {
		TestPresets []struct {
			Name            string `json:"name"`
			ConfigurePreset string `json:"configurePreset"`
			Configuration   string `json:"configuration"`
			Output          struct {
				OutputOnFailure bool `json:"outputOnFailure"`
			} `json:"output"`
		} `json:"testPresets"`
	}
	if err := json.Unmarshal([]byte(out), &pf); err != nil {
		t.Fatal(err)
	}
	// debug, release, and one per sanitizer: every configure preset is usable
	// from ctest, or 'cppm test --sanitize=tsan' has nothing to drive.
	if want := 2 + len(safety.Sanitizers); len(pf.TestPresets) != want {
		t.Fatalf("expected %d test presets (debug, release and one per sanitizer), got %d", want, len(pf.TestPresets))
	}
	for _, tp := range pf.TestPresets {
		if tp.ConfigurePreset != tp.Name {
			t.Errorf("test preset %q should use the configure preset of the same name", tp.Name)
		}
		// Multi-config generators ignore CMAKE_BUILD_TYPE and pick at test time.
		if tp.Configuration == "" {
			t.Errorf("test preset %q has no configuration, so Visual Studio and Xcode cannot use it", tp.Name)
		}
		// A failing test whose output is hidden reports only that something broke.
		if !tp.Output.OutputOnFailure {
			t.Errorf("test preset %q should print output on failure", tp.Name)
		}
	}
}

// --- sourceGlobs ---------------------------------------------------------

func globManifest() *manifest.Manifest {
	m := manifest.New("MyApp", 20)
	t := m.Targets["MyApp"]
	t.SourceGlobs = []string{"src/net"}
	m.Targets["MyApp"] = t
	return m
}

// A project with no patterns must produce exactly what it produced before the
// feature existed: no stray variables, and no deferred call.
func TestManagedOmitsSourceBlockWithoutGlobs(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	// Named precisely rather than as a bare "cmake_language(DEFER": the
	// safety section defers a call of its own, and it is entitled to.
	for _, s := range []string{
		"_cppm_glob_targets",
		"cmake_language(DEFER CALL _cppm_apply_globbed_sources",
		"target_sources",
	} {
		if strings.Contains(out, s) {
			t.Errorf("a project with no sourceGlobs must not emit %q", s)
		}
	}
}

func TestManagedAttachesGlobbedSourcesToTheTarget(t *testing.T) {
	out := Managed(globManifest(), sampleLock(), "test", GlobbedSources{
		"MyApp": {"src/net/resolver.cpp", "src/net/socket.cpp"},
	})

	for _, want := range []string{
		"set(_cppm_glob_targets MyApp)",
		`"${CMAKE_CURRENT_SOURCE_DIR}/src/net/socket.cpp"`,
		"target_sources(${_cppm_gt} PRIVATE ${_cppm_glob_sources_${_cppm_gt}})",
		"cmake_language(DEFER CALL _cppm_apply_globbed_sources)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated CMake is missing %q", want)
		}
	}

	// The whole point of deferring: the include runs before add_executable(),
	// so a direct call would fail on a target that does not exist yet.
	deferAt := strings.Index(out, "cmake_language(DEFER CALL")
	fnAt := strings.Index(out, "function(_cppm_apply_globbed_sources)")
	if fnAt < 0 || deferAt < fnAt {
		t.Error("the deferred function must be defined before it is deferred")
	}
}

// An empty entry would make the generated FATAL_ERROR fire for a target that
// merely matched no files, so sync must leave such a target out entirely.
func TestManagedIgnoresTargetsWithNoMatchedFiles(t *testing.T) {
	out := Managed(globManifest(), sampleLock(), "test", GlobbedSources{"MyApp": {}})
	if strings.Contains(out, "_cppm_glob_targets") {
		t.Error("a target whose patterns matched nothing must not reach the generated CMake")
	}
}

func TestManagedSourceBlockIsDeterministic(t *testing.T) {
	g := GlobbedSources{"MyApp": {"src/net/a.cpp", "src/net/b.cpp"}}
	first := Managed(globManifest(), sampleLock(), "test", g)
	for i := 0; i < 5; i++ {
		if again := Managed(globManifest(), sampleLock(), "test", g); again != first {
			t.Fatal("regenerating with the same input produced different output")
		}
	}
}

// The error has to name the file to edit and the command to run: cppm's users
// do not know CMake, and a target that vanished from CMakeLists.txt is the one
// way this feature breaks.
func TestManagedSourceBlockExplainsAMissingTarget(t *testing.T) {
	out := Managed(globManifest(), sampleLock(), "test", GlobbedSources{"MyApp": {"src/net/a.cpp"}})
	i := strings.Index(out, "FATAL_ERROR")
	for i >= 0 {
		block := out[i:]
		if end := strings.Index(block, "endif()"); end > 0 {
			block = block[:end]
		}
		if strings.Contains(block, "sourceGlobs") {
			for _, want := range []string{"CMakeLists.txt", "add_executable", "cppm sources remove"} {
				if !strings.Contains(block, want) {
					t.Errorf("the missing-target error does not mention %q", want)
				}
			}
			return
		}
		next := strings.Index(out[i+1:], "FATAL_ERROR")
		if next < 0 {
			break
		}
		i += 1 + next
	}
	t.Error("no FATAL_ERROR covers a target that sourceGlobs names but CMakeLists.txt does not define")
}

// The scaffold is where a user goes when they create their second .cpp, so it
// has to answer "where do I add it" on its own. A source file that is never
// compiled fails at link time with an undefined symbol, which names neither
// CMake nor this list.
func TestCMakeListsExplainsBothWaysToAddSources(t *testing.T) {
	out := CMakeLists(manifest.New("MyApp", 20))
	for _, want := range []string{
		"cppm sources add",           // the wildcard route
		`"src/**/*.cpp"`,             // and that ** spans directories
		".cppm/generated/cppm.cmake", // where the expansion lands
		"cppm sync",                  // and that it costs a sync
		"#     target_include_directories(MyApp PRIVATE include src)", // headers
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the scaffold does not mention %q, so it does not explain how to add a file", want)
		}
	}

	// The header path itself stays as it was: README documents that init
	// writes 'include' and that adding 'src' is the user's edit. The comment
	// teaches that edit, it does not make it.
	if !strings.Contains(out, "\ntarget_include_directories(MyApp PRIVATE include)\n") {
		t.Error("init must still generate 'PRIVATE include'; adding 'src' is the user's call, per README")
	}
}

// Every added line is a comment. The guide is prose inside a file CMake parses,
// and a stray uncommented line would be a configure error in every new project.
func TestCMakeListsGuideIsAllComments(t *testing.T) {
	out := CMakeLists(withTests())
	in := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "# ---") {
			in = !in
			continue
		}
		if in && !strings.HasPrefix(line, "#") {
			t.Fatalf("guide line is not commented, CMake will fail on it: %q", line)
		}
	}
}

// 'cppm sources add' resolves the target implicitly only while there is one
// target, and --with-tests already makes two. An example that errors as typed
// teaches the user that the tool is broken.
func TestCMakeListsSourceExamplesWorkAsTyped(t *testing.T) {
	solo := CMakeLists(manifest.New("MyApp", 20))
	if strings.Contains(solo, "cppm sources add --target") {
		t.Error("single-target scaffold shows --target, which is noise: the flag is optional there")
	}

	multi := CMakeLists(withTests())
	for _, line := range strings.Split(multi, "\n") {
		trimmed := strings.TrimLeft(strings.TrimPrefix(line, "#"), " ")
		if !strings.HasPrefix(trimmed, "cppm sources add") && !strings.HasPrefix(trimmed, "cppm sources remove") {
			continue
		}
		if !strings.Contains(trimmed, "--target") {
			t.Errorf("this project has two targets, so %q fails with 'cppm sources: --target is required'", trimmed)
		}
	}
}

// Written once, against the first target. Repeating it per target is how a
// scaffold becomes a wall of comments nobody reads, including the parts that
// matter.
func TestCMakeListsGuideIsNotRepeatedPerTarget(t *testing.T) {
	out := CMakeLists(withTests())
	if n := strings.Count(out, "how to add source files to them"); n != 1 {
		t.Errorf("the sources guide appears %d times; it belongs once, above the first target", n)
	}
	if n := strings.Count(out, `Where #include "..." searches`); n != 1 {
		t.Errorf("the headers guide appears %d times; it belongs once", n)
	}
}

// The flags must never reach a dependency. add_compile_options() in the
// generated file would set them for this directory and every directory added
// below it -- and FetchContent adds every dependency as a subdirectory, so
// -Werror would be enforced on fmt and spdlog, where a warning is not the
// user's bug and an upstream release can stop their build.
//
// This is the test that matters in this section. The others check wording; this
// one checks that the feature is not actively harmful.
func TestSafetyFlagsNeverReachDependencies(t *testing.T) {
	m := withTests()
	m.Safety = &manifest.Safety{Warnings: safety.WarningsError}
	out := Managed(m, sampleLock(), "test", nil)

	// The code, not the comments: the section's own explanation names
	// add_compile_options() as the thing it is avoiding.
	code := cmakeCode(out)
	for _, forbidden := range []string{
		"add_compile_options(",
		"add_link_options(",
		"add_definitions(",
	} {
		if strings.Contains(code, forbidden) {
			t.Errorf("generated CMake uses %s, which applies to every dependency added below it; "+
				"the safety flags must go on the project's own targets only", forbidden)
		}
	}

	// CMAKE_CXX_FLAGS *is* used, by the sanitizer section, which must reach the
	// dependencies. So the invariant is stated on the flags rather than on the
	// mechanism: a warning flag may appear only while building
	// _cppm_warning_options, which is applied per target and PRIVATE.
	for _, stmt := range cmakeStatements(code) {
		if !mentionsWarningFlag(stmt) {
			continue
		}
		if !strings.Contains(stmt, "_cppm_warning_options") {
			t.Errorf("a warning flag escapes the per-target list and will reach the dependencies: %s", stmt)
		}
	}

	// And the flags must be PRIVATE: in a library's INTERFACE compile options
	// they would be inherited by everything that links it.
	if !strings.Contains(out, "target_compile_options(${_cppm_st} PRIVATE ${_cppm_warning_options})") {
		t.Error("warning flags are not applied per target with PRIVATE visibility")
	}
	if strings.Contains(out, "target_compile_options(${_cppm_st} PUBLIC") ||
		strings.Contains(out, "target_compile_options(${_cppm_st} INTERFACE") {
		t.Error("warning flags must be PRIVATE, or every consumer of this project inherits them")
	}
}

// The level in cppm.json chooses the flags, and only "error" may fail a build.
func TestSafetyWarningLevelsRenderTheirFlags(t *testing.T) {
	cases := []struct {
		level   string
		want    []string
		notWant []string
	}{
		{safety.WarningsDefault, []string{"-Wall", "-Wextra", "/W3"}, []string{"-Werror", "/WX", "-Wshadow"}},
		{safety.WarningsStrict, []string{"-Wall", "-Wshadow", "-Wconversion", "/W4", "/permissive-"}, []string{"-Werror", "/WX"}},
		{safety.WarningsError, []string{"-Werror", "/WX", "-Wshadow"}, nil},
	}
	for _, c := range cases {
		m := manifest.New("MyApp", 20)
		m.Safety = &manifest.Safety{Warnings: c.level}
		code := cmakeCode(Managed(m, sampleLock(), "test", nil))
		for _, w := range c.want {
			if !strings.Contains(code, w) {
				t.Errorf("level %q does not emit %q", c.level, w)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(code, w) {
				t.Errorf("level %q emits %q, which belongs to a stricter level", c.level, w)
			}
		}
	}
}

// "off" plus hardening off must emit nothing at all, not an empty section.
// It is the escape hatch for a codebase that cannot yet survive -Wall, and an
// escape hatch that still costs two try_compile probes per configure is not
// one.
func TestSafetyOffEmitsNoSection(t *testing.T) {
	m := manifest.New("MyApp", 20)
	no := false
	m.Safety = &manifest.Safety{Warnings: safety.WarningsOff, Hardening: &no}
	out := Managed(m, sampleLock(), "test", nil)

	for _, forbidden := range []string{
		"_cppm_safety_targets",
		"CheckCXXCompilerFlag",
		"_cppm_apply_safety",
	} {
		if strings.Contains(out, forbidden) {
			t.Errorf("warnings off with hardening off still emits %q", forbidden)
		}
	}
}

// Hardening on with warnings off is a real combination -- release hardening
// without touching a noisy existing codebase -- so one must not imply the
// other.
func TestHardeningIsIndependentOfWarnings(t *testing.T) {
	m := manifest.New("MyApp", 20)
	m.Safety = &manifest.Safety{Warnings: safety.WarningsOff}
	out := Managed(m, sampleLock(), "test", nil)

	if !strings.Contains(out, "-D_GLIBCXX_ASSERTIONS") {
		t.Error("hardening defaults to on and must survive warnings being off")
	}
	if strings.Contains(out, "-Wall") {
		t.Error("warnings off must emit no warning flags even when hardening is on")
	}

	yes := true
	m.Safety = &manifest.Safety{Warnings: safety.WarningsStrict, Hardening: &yes}
	hardened := Managed(m, sampleLock(), "test", nil)
	no := false
	m.Safety = &manifest.Safety{Warnings: safety.WarningsStrict, Hardening: &no}
	soft := Managed(m, sampleLock(), "test", nil)

	if !strings.Contains(hardened, "-fstack-protector-strong") {
		t.Error("hardening on must probe the stack protector")
	}
	if strings.Contains(soft, "-fstack-protector-strong") || strings.Contains(soft, "_FORTIFY_SOURCE") {
		t.Error("hardening off must emit no hardening flags")
	}
	if !strings.Contains(soft, "-Wall") {
		t.Error("hardening off must leave the warnings alone")
	}
}

// _FORTIFY_SOURCE is implemented in glibc's headers via __builtin_object_size,
// which computes nothing until the optimiser runs: at -O0 it silently does
// nothing and, on a distro that predefines it, warns on every file. So it has
// to be guarded by configuration rather than applied flat.
func TestFortifySourceIsGuardedByConfiguration(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)

	for _, line := range strings.Split(cmakeCode(out), "\n") {
		if !strings.Contains(line, "_FORTIFY_SOURCE") || strings.HasPrefix(line, "set(_cppm_optimised") {
			continue
		}
		if !strings.Contains(line, "$<${_cppm_optimised}:") {
			t.Errorf("_FORTIFY_SOURCE is applied unguarded, so it does nothing in a debug build "+
				"and warns about a redefined macro on Ubuntu and Fedora: %s", line)
		}
	}
	// And the guard itself must name the optimised configurations. "not Debug"
	// is true for an empty configuration -- a multi-config generator, or no
	// CMAKE_BUILD_TYPE -- which is the unoptimised case the flag does nothing
	// in.
	if !strings.Contains(out, `set(_cppm_optimised "$<OR:$<CONFIG:Release>,`) {
		t.Error("the _FORTIFY_SOURCE guard does not name the optimised configurations")
	}
	if strings.Contains(cmakeCode(out), "$<NOT:$<CONFIG:Debug>>") {
		t.Error("the optimised configurations must be named, not written as \"not Debug\": " +
			"an empty configuration satisfies \"not Debug\"")
	}
	if !strings.Contains(out, "-U_FORTIFY_SOURCE") {
		t.Error("the undefine is missing, so Ubuntu and Fedora builds warn on every translation unit")
	}
}

// A genex carrying a literal semicolon is split in half by the unquoted
// ${_cppm_harden_options} expansion, producing two fragments that are not valid
// flags. One genex per flag is what avoids it.
func TestGeneratorExpressionsCarryNoSemicolon(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, "$<") {
			continue
		}
		if i := strings.Index(trimmed, "$<"); i >= 0 && strings.Contains(trimmed[i:], ";") {
			t.Errorf("a generator expression contains a semicolon and will be split by list expansion: %s", trimmed)
		}
	}
}

// Flags that are a hard error on aarch64 must be put to the compiler first.
// Hardcoding -fcf-protection means cppm generates projects that do not compile
// on an Apple M-series machine or a Raspberry Pi.
func TestUnportableHardeningFlagsAreProbed(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)

	if !strings.Contains(out, "include(CheckCXXCompilerFlag)") {
		t.Fatal("hardening flags are emitted without including CheckCXXCompilerFlag")
	}
	if !strings.Contains(out, "include(CheckLinkerFlag)") {
		t.Fatal("hardening link flags are emitted without including CheckLinkerFlag")
	}
	for _, flag := range []string{"-fstack-clash-protection", "-fcf-protection", "-Wl,-z,relro"} {
		probed := "_cppm_append_supported_compile_flag(_cppm_harden_options " + flag + ")"
		probedLink := "_cppm_append_supported_link_flag(_cppm_harden_link_options " + flag + ")"
		if !strings.Contains(out, probed) && !strings.Contains(out, probedLink) {
			t.Errorf("%q is not behind a probe; it is a hard error on some targets", flag)
		}
	}
}

// Every target in cppm.json gets the flags, test targets included: a test is
// code too, and it is where the bugs are being hunted.
func TestSafetyCoversTestTargets(t *testing.T) {
	out := Managed(withTests(), sampleLock(), "test", nil)
	if !strings.Contains(out, "set(_cppm_safety_targets MyApp MyApp_tests)") {
		t.Error("the test target must receive the safety flags too")
	}
}

// An INTERFACE library has no sources of its own and target_compile_options()
// rejects PRIVATE on one outright, which would be a configure error in any
// project whose library target is header-only.
func TestSafetySkipsInterfaceLibraries(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	if !strings.Contains(out, "INTERFACE_LIBRARY") {
		t.Error("no guard against an INTERFACE library, where PRIVATE compile options are a configure error")
	}
}

// The flags are attached by a deferred call for the same reason the globbed
// sources are: this file is included before the user's add_executable() runs,
// and cppm does not edit CMakeLists.txt. The alternative was a new public
// variable, which cppm could never migrate.
func TestSafetyIsAppliedByADeferredCall(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	if !strings.Contains(out, "cmake_language(DEFER CALL _cppm_apply_safety)") {
		t.Error("the safety flags are not deferred, so they run before the targets exist")
	}
}

// The generated file records the level it was generated with, in a form 'cppm
// sync' can read back: that line is the only record of what the build tree was
// configured with, and the change report reads it to say "warnings default ->
// strict" rather than "regenerated".
func TestSafetySectionRecordsItsLevel(t *testing.T) {
	m := manifest.New("MyApp", 20)
	m.Safety = &manifest.Safety{Warnings: safety.WarningsStrict}
	out := Managed(m, sampleLock(), "test", nil)
	if !strings.Contains(out, `"warnings": "strict"`) {
		t.Error("the generated file does not record its warning level, so sync cannot report a change to it")
	}
}

// The section must stay out of the way of the rule it exists to respect:
// nothing in the generated file may be emitted before FetchContent_Declare
// that changes how the dependencies are built.
func TestSafetySectionComesAfterTheDependencies(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	fetch := strings.Index(out, "FetchContent_Declare")
	safetySection := strings.Index(out, "set(_cppm_safety_targets")
	if fetch < 0 || safetySection < 0 {
		t.Fatal("expected both a FetchContent_Declare and a safety section")
	}
	if safetySection < fetch {
		t.Error("the safety section is emitted before the dependencies; it must not be able to influence how they build")
	}
}

// cmakeCode strips comment lines from generated CMake.
//
// Needed because the generated file explains the mistakes it avoids, and names
// them: the safety section's own comment says why add_compile_options() and
// -Werror on dependencies are wrong. A test searching the raw text would find
// the explanation and report the bug it warns against.
func cmakeCode(out string) string {
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		kept = append(kept, trimmed)
	}
	return strings.Join(kept, "\n")
}

// mentionsWarningFlag reports whether a line of generated CMake carries a
// compiler warning flag, in either GCC or MSVC spelling.
func mentionsWarningFlag(line string) bool {
	for _, f := range []string{"-Wall", "-Wextra", "-Wpedantic", "-Wshadow", "-Wconversion",
		"-Wold-style-cast", "-Werror", "/W3", "/W4", "/WX", "/permissive-"} {
		if strings.Contains(line, f) {
			return true
		}
	}
	return false
}

// cmakeStatements joins the continuation lines of a multi-line CMake command
// into one string each, by paren balance.
//
// A per-line check cannot see that
//
//	list(APPEND _cppm_warning_options
//	    -Wall
//	    -Werror)
//
// is one statement, and would report -Werror as loose.
func cmakeStatements(code string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	for _, line := range strings.Split(code, "\n") {
		if cur.Len() > 0 {
			cur.WriteString(" ")
		}
		cur.WriteString(strings.TrimSpace(line))
		depth += strings.Count(line, "(") - strings.Count(line, ")")
		if depth <= 0 {
			out = append(out, cur.String())
			cur.Reset()
			depth = 0
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// The mirror image of TestSafetyFlagsNeverReachDependencies, and the pair is
// the point. Warnings must not reach a dependency; sanitizers must. ASan and
// TSan instrument the whole program or they are unreliable -- an
// uninstrumented dependency gives ASan a memcpy it cannot check and makes TSan
// report races whose synchronisation it cannot see.
func TestSanitizerFlagsDoReachDependencies(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)

	// Set before the dependencies, which is what makes them inherit it.
	// Emitting this after FetchContent_MakeAvailable would be inert, the same
	// hazard as a cache option emitted after FetchContent_Declare.
	flags := strings.Index(out, `set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} ${_cppm_san_flags}")`)
	fetch := strings.Index(out, "FetchContent_Declare")
	if flags < 0 {
		t.Fatal("the sanitizer flags never reach CMAKE_CXX_FLAGS, so the dependencies are not instrumented")
	}
	if fetch < 0 || flags > fetch {
		t.Error("the sanitizer flags are set after the dependencies are declared, where they are inert")
	}

	// Compiled in but not linked fails with undefined references to __asan_*,
	// which is the most common way this gets half-done.
	if !strings.Contains(out, "CMAKE_${_cppm_lf}_LINKER_FLAGS") {
		t.Error("the sanitizer is not added to the linker flags; the build will fail on __asan_* symbols")
	}
	for _, kind := range []string{"EXE", "SHARED", "MODULE"} {
		if !strings.Contains(out, kind) {
			t.Errorf("linker flags are not set for %s targets", kind)
		}
	}
}

// Inert in an ordinary build. The section is always emitted -- it is driven by
// a cache variable a preset sets -- so it must add nothing at all when that
// variable is empty.
func TestSanitizerSectionIsInertWithoutAPreset(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	guard := "if(" + safety.SanitizerCacheVar + ")"
	if !strings.Contains(out, guard) {
		t.Fatalf("the sanitizer section is not guarded by %s", guard)
	}
	// Every -fsanitize in the file has to sit inside that guard, or a plain
	// 'cppm build' is instrumented without being asked for.
	block := cmakeIfBlock(cmakeCode(out), guard)
	if block == "" {
		t.Fatalf("could not find the %s block", guard)
	}
	inside := strings.Count(block, "-fsanitize") + strings.Count(block, "/fsanitize")
	total := strings.Count(cmakeCode(out), "-fsanitize") + strings.Count(cmakeCode(out), "/fsanitize")
	if inside != total {
		t.Errorf("%d of %d sanitizer flags are outside %s, so an ordinary build is instrumented", total-inside, total, guard)
	}
}

// MSVC has AddressSanitizer and nothing else. Requesting UBSan or TSan there
// must fail with that sentence, not with an unrecognised-option error from the
// compiler that names neither cppm nor the preset.
func TestMSVCRefusesTheSanitizersItCannotDo(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)

	// Only the then-branch. if(MSVC)...else()...endif() is one block, so
	// taking the whole block would include the GCC/Clang flags in the else.
	msvc := cmakeThenBranch(cmakeCode(out), "if(MSVC)")
	if msvc == "" {
		t.Fatal("no if(MSVC) branch in the sanitizer section")
	}
	if !strings.Contains(msvc, "/fsanitize=address") {
		t.Error("MSVC does support AddressSanitizer; the MSVC branch must offer it")
	}
	// Anything MSVC cannot do falls to one refusal that names what was asked
	// for, rather than a copy of the same message per sanitizer.
	if !strings.Contains(msvc, "FATAL_ERROR") {
		t.Error("the MSVC branch must refuse what MSVC cannot do, not fall through silently")
	}
	if !strings.Contains(msvc, "${"+safety.SanitizerCacheVar+"} is not available under MSVC") {
		t.Error("the refusal must name the sanitizer that was requested")
	}
	if !strings.Contains(msvc, "MSVC implements AddressSanitizer only") {
		t.Error("the refusal must say why, or the user tries the same thing again")
	}
	if !strings.Contains(msvc, "--sanitize=asan") {
		t.Error("the refusal must name the one sanitizer that does work there")
	}
	// No GNU-style flag may reach MSVC: it would arrive as an unrecognised
	// option, which is the failure this branch exists to replace.
	if strings.Contains(msvc, "-fsanitize=") {
		t.Error("a GCC/Clang sanitizer flag is in the MSVC branch")
	}
}

// A sanitizer preset must not get a CMAKE_BUILD_TYPE. The sanitizer flags
// carry -g -O1 themselves and a build type's own -O0 or -O2 comes later on the
// command line, so it would win; and no build type also means no -DNDEBUG, so
// assert() stays live, and the _FORTIFY_SOURCE genex stays off -- fortified
// libc calls hide the overflows ASan is there to catch.
func TestSanitizerPresetsSetNoBuildType(t *testing.T) {
	out, err := Presets(manifest.New("MyApp", 20))
	if err != nil {
		t.Fatal(err)
	}
	var pf struct {
		ConfigurePresets []struct {
			Name           string            `json:"name"`
			BinaryDir      string            `json:"binaryDir"`
			DisplayName    string            `json:"displayName"`
			CacheVariables map[string]string `json:"cacheVariables"`
		} `json:"configurePresets"`
		TestPresets []struct {
			Name        string            `json:"name"`
			Environment map[string]string `json:"environment"`
		} `json:"testPresets"`
	}
	if err := json.Unmarshal([]byte(out), &pf); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, p := range pf.ConfigurePresets {
		if !safety.ValidSanitizer(p.Name) {
			continue
		}
		seen[p.Name] = true
		if bt, ok := p.CacheVariables["CMAKE_BUILD_TYPE"]; ok && bt != "" {
			t.Errorf("sanitizer preset %q sets CMAKE_BUILD_TYPE=%q, whose optimisation level overrides the -O1 the sanitizer flags ask for", p.Name, bt)
		}
		if got := p.CacheVariables[safety.SanitizerCacheVar]; got != p.Name {
			t.Errorf("sanitizer preset %q sets %s=%q, want %q", p.Name, safety.SanitizerCacheVar, got, p.Name)
		}
		// Its own build tree. Mixing instrumented and uninstrumented objects
		// in one tree links successfully and then crashes inside the
		// sanitizer's runtime.
		if !strings.HasSuffix(p.BinaryDir, "/"+p.Name) {
			t.Errorf("sanitizer preset %q builds into %q, which it must not share with another preset", p.Name, p.BinaryDir)
		}
		// cmake --list-presets prints displayName, which is how somebody
		// finds these without reading cppm's documentation first.
		if p.DisplayName == p.Name || p.DisplayName == "" {
			t.Errorf("sanitizer preset %q has no explanatory displayName", p.Name)
		}
	}
	for _, name := range safety.Sanitizers {
		if !seen[name] {
			t.Errorf("no configure preset for %q", name)
		}
	}

	// The runtime options go on the test preset, because they are read by the
	// process under test rather than the compiler.
	for _, tp := range pf.TestPresets {
		if !safety.ValidSanitizer(tp.Name) {
			continue
		}
		if len(tp.Environment) == 0 {
			t.Errorf("test preset %q sets no sanitizer runtime options, so it reports and carries on", tp.Name)
		}
	}
}

// The MEMORYCHECK_* variables must be set before include(CTest): the CTest
// module reads them to write DartConfiguration.tcl, which is the only thing
// `ctest -T memcheck` looks at. Setting them afterwards is inert, and the
// failure is a memcheck run with no valgrind in it.
func TestValgrindWiringPrecedesIncludeCTest(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	mem := strings.Index(out, "set(MEMORYCHECK_COMMAND ")
	ctest := strings.Index(out, "include(CTest)")
	if mem < 0 {
		t.Fatal("no MEMORYCHECK_COMMAND, so 'ctest -T memcheck' has no valgrind to run")
	}
	if ctest < 0 || mem > ctest {
		t.Error("the MEMORYCHECK_* variables are set after include(CTest), where the CTest module has already written DartConfiguration.tcl")
	}
}

// find_program must not write straight into MEMORYCHECK_COMMAND. When valgrind
// is absent that leaves the cache entry NOTFOUND, and the CTest module's own
// find_program then searches the same variable over a NAMES list that includes
// NVIDIA's compute-sanitizer -- so a machine with the CUDA toolkit ends up
// running the wrong tool with valgrind's options.
func TestValgrindSearchUsesAPrivateVariable(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	if strings.Contains(out, "find_program(MEMORYCHECK_COMMAND") {
		t.Error("find_program writes into MEMORYCHECK_COMMAND; on a CUDA machine the CTest module " +
			"then fills it with compute-sanitizer while MEMORYCHECK_TYPE still says Valgrind")
	}
	if !strings.Contains(out, "find_program(_cppm_valgrind") {
		t.Error("expected the valgrind search to use a private variable")
	}
	// And nothing may be claimed unless valgrind itself was found.
	code := cmakeCode(out)
	guard := strings.Index(code, "if(_cppm_valgrind)")
	typ := strings.Index(code, "set(MEMORYCHECK_TYPE Valgrind")
	if guard < 0 || typ < 0 || typ < guard {
		t.Error("MEMORYCHECK_TYPE is set outside the if(_cppm_valgrind) guard, claiming valgrind on a machine without it")
	}
}

// A sanitizer-instrumented tree must refuse valgrind even when ctest is driven
// by hand, because the two intercept the same allocations and each reports the
// other's bookkeeping as an error.
func TestInstrumentedTreeDisablesValgrind(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	want := "if(" + safety.SanitizerCacheVar + " AND _cppm_valgrind)"
	if !strings.Contains(out, want) {
		t.Errorf("expected %s, so a sanitizer build tree cannot be driven under valgrind by a bare ctest", want)
	}
}

// The scaffolded suppressions file has to teach what a suppression is. An empty
// one is no better than none: the first memcheck run on any real project
// reports errors inside glibc, and somebody who does not know suppressions
// exist concludes the tool is broken or their code is.
func TestValgrindSuppExplainsItself(t *testing.T) {
	supp := ValgrindSupp()
	for _, want := range []string{
		"--gen-suppressions",
		"Memcheck:Leak",
		"obj:",
		"...",
	} {
		if !strings.Contains(supp, want) {
			t.Errorf("the scaffolded %s does not mention %q", safety.SuppressionsFile, want)
		}
	}
	// Every line is a comment: valgrind parses this file, and a stray
	// uncommented line is a parse error on every memcheck run.
	for _, line := range strings.Split(supp, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		t.Errorf("the scaffolded suppressions file has a non-comment line, which valgrind will try to parse: %q", line)
	}
}

// cmakeIfBlock returns the text of the if() block that starts with header,
// up to its matching endif(), counting nesting.
//
// A flat search cannot tell an assignment inside a guard from one outside it,
// which is the whole question for the sanitizer section.
func cmakeIfBlock(code, header string) string {
	start := strings.Index(code, header)
	if start < 0 {
		return ""
	}
	lines := strings.Split(code[start:], "\n")
	depth := 0
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "if("):
			depth++
		case strings.HasPrefix(line, "endif"):
			depth--
			if depth == 0 {
				return strings.Join(lines[:i+1], "\n")
			}
		}
	}
	return ""
}

// cmakeThenBranch returns the body of an if() up to its own else(), counting
// nesting so an inner else() does not end it.
//
// The then-branch, not the whole block: if(MSVC)...else()...endif() is one
// block, and a test asking "what does MSVC get" must not see the else.
func cmakeThenBranch(code, header string) string {
	start := strings.Index(code, header)
	if start < 0 {
		return ""
	}
	lines := strings.Split(code[start:], "\n")
	depth := 1
	for i, line := range lines[1:] {
		switch {
		case strings.HasPrefix(line, "if("):
			depth++
		case strings.HasPrefix(line, "endif"):
			depth--
			if depth == 0 {
				return strings.Join(lines[:i+2], "\n")
			}
		case strings.HasPrefix(line, "else"):
			if depth == 1 {
				return strings.Join(lines[:i+2], "\n")
			}
		}
	}
	return ""
}

// /RTC1 is in CMake's default MSVC Debug flags, and MSVC rejects it alongside
// /fsanitize=address. The sanitizer build presets are picked up as Debug by a
// multi-config generator, so this is the default case on Windows rather than
// an edge one.
//
// Not verified against a real MSVC -- cppm has no Windows CI yet -- so this
// test guards the generated text, and the Windows caveat is recorded in the
// roadmap.
func TestMSVCSanitizerStripsRuntimeChecks(t *testing.T) {
	out := Managed(manifest.New("MyApp", 20), sampleLock(), "test", nil)
	block := cmakeIfBlock(cmakeCode(out), "if("+safety.SanitizerCacheVar+")")
	if block == "" {
		t.Fatal("no sanitizer section")
	}
	if !strings.Contains(block, "/RTC") {
		t.Error("the sanitizer section does not strip /RTC1, which MSVC rejects with /fsanitize=address")
	}
	if !strings.Contains(block, "CMAKE_CXX_FLAGS_${_cppm_cfg}") {
		t.Error("/RTC is stripped from the wrong variable: CMake puts it in the per-configuration flags")
	}
}
