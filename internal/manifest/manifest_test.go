package manifest

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNewIsValid(t *testing.T) {
	m := New("MyApp", 20)
	if err := m.Validate(); err != nil {
		t.Fatalf("New produced an invalid manifest: %v", err)
	}
	if m.CXX.Extensions {
		t.Error("cxx.extensions must default to false: CMake defaults it ON, which silently compiles as gnu++NN")
	}
	if !m.CXX.Required {
		t.Error("cxx.required must default to true")
	}
}

func TestRoundTrip(t *testing.T) {
	m := New("MyApp", 23)
	m.Dependencies["fmt"] = Dependency{
		Source:  SourceGit,
		URL:     "https://github.com/fmtlib/fmt.git",
		Version: "11.1.4",
		Targets: []string{"fmt::fmt"},
		Options: map[string]string{"FMT_TEST": "OFF"},
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := m.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != m.Name || got.CXX.Standard != 23 {
		t.Errorf("round trip changed values: %+v", got)
	}
	d, ok := got.Dependencies["fmt"]
	if !ok {
		t.Fatal("dependency lost in round trip")
	}
	if d.Version != "11.1.4" || d.Targets[0] != "fmt::fmt" || d.Options["FMT_TEST"] != "OFF" {
		t.Errorf("dependency fields changed: %+v", d)
	}
}

// Serialisation must be byte-stable, or every sync produces a spurious diff.
func TestSerialisationIsDeterministic(t *testing.T) {
	build := func() *Manifest {
		m := New("MyApp", 20)
		for _, n := range []string{"zlib", "fmt", "json", "abseil", "spdlog"} {
			m.Dependencies[n] = Dependency{
				Source:  SourceGit,
				URL:     "https://example.invalid/" + n + ".git",
				Options: map[string]string{"B": "0", "A": "1", "C": "2"},
			}
		}
		return m
	}
	first, err := build().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := build().Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("output differs between runs on iteration %d", i)
		}
	}
	if !strings.HasSuffix(string(first), "\n") {
		t.Error("output must end with a newline")
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Manifest)
		want string
	}{
		{"empty name", func(m *Manifest) { m.Name = "" }, "invalid project name"},
		{"name starts with digit", func(m *Manifest) { m.Name = "9lives" }, "invalid project name"},
		{"bad standard", func(m *Manifest) { m.CXX.Standard = 21 }, "invalid cxx.standard"},
		{"no cmake minimum", func(m *Manifest) { m.CMake.Minimum = "" }, "cmake.minimum"},
		{"bad schema", func(m *Manifest) { m.Schema = 99 }, "unsupported schema"},
		{"unknown source", func(m *Manifest) {
			m.Dependencies["x"] = Dependency{Source: "svn"}
		}, "unknown source"},
		{"git without url", func(m *Manifest) {
			m.Dependencies["x"] = Dependency{Source: SourceGit}
		}, "requires a url"},
		{"path without path", func(m *Manifest) {
			m.Dependencies["x"] = Dependency{Source: SourcePath}
		}, "requires a path"},
		{"git with path", func(m *Manifest) {
			m.Dependencies["x"] = Dependency{Source: SourceGit, URL: "u", Path: "p"}
		}, "must not set path"},
		{"target kind", func(m *Manifest) {
			m.Targets["MyApp"] = Target{Kind: "wat", Sources: []string{"a.cpp"}}
		}, "invalid kind"},
		{"target without sources", func(m *Manifest) {
			m.Targets["MyApp"] = Target{Kind: KindExecutable}
		}, "at least one source"},
		{"target depends on unknown", func(m *Manifest) {
			m.Targets["MyApp"] = Target{Kind: KindExecutable, Sources: []string{"a.cpp"},
				Dependencies: []string{"ghost"}}
		}, "not in dependencies"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New("MyApp", 20)
			tc.mut(m)
			err := m.Validate()
			if err == nil {
				t.Fatalf("expected an error mentioning %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	bad := `{"schema":1,"name":"X","version":"0.1.0","cxx":{"standard":20,"required":true,"extensions":false},"cmake":{"minimum":"3.24"},"dependencies":{},"typo_here":1}`
	if err := writeFile(path, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected a typo'd field to be rejected rather than silently ignored")
	}
}

// init writes every setting explicitly, even at its default, so that the file
// shows what can be changed.
func TestNewWritesDefaultsExplicitly(t *testing.T) {
	m := New("MyApp", 20)
	b, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		`"exportCompileCommands": true`,
		`"buildTypes"`,
		`"debug": "Debug"`,
		`"release": "RelWithDebInfo"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("a new manifest should spell out %s:\n%s", want, out)
		}
	}
}

// An absent field must mean "default", not "false"; otherwise a hand-written
// manifest that omits it would silently lose compile_commands.json.
func TestAbsentCompileCommandsMeansDefaultNotFalse(t *testing.T) {
	var c CMake
	if !c.WantCompileCommands() {
		t.Error("an unset exportCompileCommands must default to true")
	}
	f := false
	c.ExportCompileCommands = &f
	if c.WantCompileCommands() {
		t.Error("an explicit false must be honoured")
	}
}

func TestBuildTypeFallsBackToDefaults(t *testing.T) {
	var c CMake
	if got := c.BuildTypeFor("debug"); got != "Debug" {
		t.Errorf("debug -> %q, want Debug", got)
	}
	if got := c.BuildTypeFor("release"); got != "RelWithDebInfo" {
		t.Errorf("release -> %q, want RelWithDebInfo", got)
	}
	c.BuildTypes = map[string]string{"release": "Release"}
	if got := c.BuildTypeFor("release"); got != "Release" {
		t.Errorf("an explicit build type must win, got %q", got)
	}
	if got := c.BuildTypeFor("debug"); got != "Debug" {
		t.Errorf("an unlisted preset should still fall back, got %q", got)
	}
}

func TestValidateRejectsBadBuildTypes(t *testing.T) {
	m := New("MyApp", 20)
	m.CMake.BuildTypes = map[string]string{"debug": "Fastest"}
	if err := m.Validate(); err == nil {
		t.Error("an invalid CMake build type should be rejected")
	}
	m.CMake.BuildTypes = map[string]string{"staging": "Debug"}
	if err := m.Validate(); err == nil {
		t.Error("an unknown preset name should be rejected")
	}
}

// Invoking a C driver for C++ compiles but fails to link the standard library.
func TestNormalizeCXXAndCompanionC(t *testing.T) {
	for _, tc := range []struct{ in, cxx, c string }{
		{"clang", "clang++", "clang"},
		{"gcc", "g++", "gcc"},
		{"clang++", "clang++", "clang"},
		{"g++", "g++", "gcc"},
		{"/usr/bin/gcc", "/usr/bin/g++", "/usr/bin/gcc"},
		{"icpx", "icpx", ""},
	} {
		gotCXX, _ := NormalizeCXX(tc.in)
		if gotCXX != tc.cxx {
			t.Errorf("NormalizeCXX(%q) = %q, want %q", tc.in, gotCXX, tc.cxx)
		}
		if gotC := CompanionC(gotCXX); gotC != tc.c {
			t.Errorf("CompanionC(%q) = %q, want %q", gotCXX, gotC, tc.c)
		}
	}
}

// --- dev dependencies (roadmap 0.2.2) ---

// A test framework listed on a shipping target is a release binary with Catch2
// compiled into it. Catching that is the entire reason the dev flag exists, so
// it has to be a validation error rather than a warning.
func TestDevDependencyOnNonTestTargetIsRejected(t *testing.T) {
	m := New("MyApp", 20)
	m.Dependencies["Catch2"] = Dependency{
		Source: SourceGit, URL: "https://github.com/catchorg/Catch2.git", Version: "v3.7.1", Dev: true,
	}
	tgt := m.Targets["MyApp"]
	tgt.Dependencies = []string{"Catch2"}
	m.Targets["MyApp"] = tgt

	err := m.Validate()
	if err == nil {
		t.Fatal("a dev dependency on a non-test target must be rejected")
	}
	// The error has to say which of the two edits to make; cppm's users do not
	// know what "dev" means by itself.
	for _, want := range []string{"Catch2", "\"test\": true", "\"dev\": true"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

func TestDevDependencyOnTestTargetIsAccepted(t *testing.T) {
	m := New("MyApp", 20)
	m.Dependencies["Catch2"] = Dependency{
		Source: SourceGit, URL: "https://github.com/catchorg/Catch2.git", Version: "v3.7.1", Dev: true,
	}
	m.Targets["MyApp_tests"] = Target{
		Kind:         KindExecutable,
		Sources:      []string{"tests/MyApp_test.cpp"},
		Dependencies: []string{"Catch2"},
		Test:         true,
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("a dev dependency on a test target is legal: %v", err)
	}
}

func TestDevDependencyNames(t *testing.T) {
	m := New("MyApp", 20)
	m.Dependencies["fmt"] = Dependency{Source: SourceGit, URL: "https://example.com/fmt.git"}
	m.Dependencies["Catch2"] = Dependency{Source: SourceGit, URL: "https://example.com/c.git", Dev: true}
	m.Dependencies["benchmark"] = Dependency{Source: SourceGit, URL: "https://example.com/b.git", Dev: true}

	// Sorted, because this is rendered into a committed file.
	if got := m.DevDependencyNames(); !reflect.DeepEqual(got, []string{"Catch2", "benchmark"}) {
		t.Errorf("dev dependencies = %v, want [Catch2 benchmark]", got)
	}
}

func TestTestTargetNames(t *testing.T) {
	m := New("MyApp", 20)
	m.Targets["z_tests"] = Target{Kind: KindExecutable, Sources: []string{"z.cpp"}, Test: true}
	m.Targets["a_tests"] = Target{Kind: KindExecutable, Sources: []string{"a.cpp"}, Test: true}
	if got := m.TestTargetNames(); !reflect.DeepEqual(got, []string{"a_tests", "z_tests"}) {
		t.Errorf("test targets = %v, want [a_tests z_tests]", got)
	}
}

// dev and test are absent from a manifest that uses neither, so adding the
// fields does not churn every existing cppm.json on the next save.
func TestDevAndTestAreOmittedWhenFalse(t *testing.T) {
	b, err := New("MyApp", 20).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"\"dev\"", "\"test\""} {
		if strings.Contains(string(b), unwanted) {
			t.Errorf("%s should be omitted when false:\n%s", unwanted, b)
		}
	}
}

// Round-tripping matters because Load uses DisallowUnknownFields: a field that
// serialises but does not parse would make cppm reject its own output.
func TestDevAndTestRoundTrip(t *testing.T) {
	m := New("MyApp", 20)
	m.Dependencies["Catch2"] = Dependency{Source: SourceGit, URL: "https://example.com/c.git", Dev: true}
	m.Targets["MyApp_tests"] = Target{
		Kind: KindExecutable, Sources: []string{"t.cpp"}, Test: true, Dependencies: []string{"Catch2"},
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dependencies["Catch2"].Dev {
		t.Error("dev was lost in the round trip")
	}
	if !got.Targets["MyApp_tests"].Test {
		t.Error("test was lost in the round trip")
	}
}
