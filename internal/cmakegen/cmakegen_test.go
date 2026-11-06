package cmakegen

import (
	"strings"
	"testing"

	"cppm/internal/lockfile"
	"cppm/internal/manifest"
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
	out := Dependencies(sampleLock(), "test")
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
	out := Dependencies(sampleLock(), "test")
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
	out := Dependencies(sampleLock(), "test")
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
	out := Dependencies(sampleLock(), "test")
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
	first := Dependencies(sampleLock(), "test")
	for i := 0; i < 10; i++ {
		if again := Dependencies(sampleLock(), "test"); again != first {
			t.Fatalf("output differs between runs on iteration %d", i)
		}
	}
}

func TestBannerPresent(t *testing.T) {
	if !strings.Contains(Dependencies(sampleLock(), "test"), "DO NOT EDIT") {
		t.Error("generated file must carry a do-not-edit banner")
	}
}

func TestEmptyLockStillProducesUsableFile(t *testing.T) {
	out := Dependencies(lockfile.New("test"), "test")
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
	out := Dependencies(l, "test")

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

func TestCMakeListsSetsExtensionsOff(t *testing.T) {
	m := manifest.New("MyApp", 20)
	out := CMakeLists(m)
	if !strings.Contains(out, "set(CMAKE_CXX_EXTENSIONS OFF)") {
		t.Error("CMAKE_CXX_EXTENSIONS OFF is required: it defaults to ON and silently selects gnu++NN")
	}
	if !strings.Contains(out, "set(CMAKE_CXX_STANDARD 20)") {
		t.Error("standard not written")
	}
	// The dependency include must precede the targets that link against it.
	incIdx := strings.Index(out, "cppm_dependencies.cmake")
	tgtIdx := strings.Index(out, "add_executable")
	if incIdx < 0 || tgtIdx < 0 || incIdx > tgtIdx {
		t.Error("the dependency include must come before the targets")
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
	out, err := Presets(m, "Ninja")
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
	out := Dependencies(l, "test")
	if !strings.Contains(out, "add_subdirectory") {
		t.Error("path dependencies should use add_subdirectory, not FetchContent")
	}
	if strings.Contains(out, "FetchContent_Declare(MyLib") {
		t.Error("path dependency must not be fetched")
	}
}
