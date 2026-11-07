package cmakegen

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mehdi-shokohi/cppm/internal/lockfile"
	"github.com/mehdi-shokohi/cppm/internal/manifest"
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
	if len(pf.TestPresets) != 2 {
		t.Fatalf("expected debug and release test presets, got %d", len(pf.TestPresets))
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
	for _, s := range []string{"_cppm_glob_targets", "cmake_language(DEFER", "target_sources"} {
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
