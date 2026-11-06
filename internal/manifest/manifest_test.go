package manifest

import (
	"path/filepath"
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
