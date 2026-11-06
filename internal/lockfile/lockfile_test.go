package lockfile

import (
	"path/filepath"
	"strings"
	"testing"
)

func pkg(name, commit string) Package {
	return Package{
		Name:         name,
		Source:       "git",
		URL:          "https://example.invalid/" + name + ".git",
		Requested:    "1.0.0",
		ResolvedRef:  "refs/tags/1.0.0",
		Commit:       commit,
		Targets:      []string{name + "::" + name},
		TargetOrigin: OriginInferred,
	}
}

// Packages must serialise in sorted order regardless of insertion order, or the
// lockfile produces noisy diffs.
func TestPackagesAreSortedAndStable(t *testing.T) {
	a := New("0.1.0")
	for _, n := range []string{"zlib", "fmt", "spdlog", "abseil"} {
		a.Put(pkg(n, strings.Repeat("a", 40)))
	}
	b := New("0.1.0")
	for _, n := range []string{"abseil", "spdlog", "fmt", "zlib"} {
		b.Put(pkg(n, strings.Repeat("a", 40)))
	}
	ab, err := a.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	bb, err := b.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(ab) != string(bb) {
		t.Error("insertion order changed the serialised lockfile")
	}
	want := []string{"abseil", "fmt", "spdlog", "zlib"}
	for i, n := range a.Names() {
		if n != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, n, want[i])
		}
	}
}

func TestPutReplacesRatherThanDuplicating(t *testing.T) {
	l := New("0.1.0")
	l.Put(pkg("fmt", strings.Repeat("a", 40)))
	l.Put(pkg("fmt", strings.Repeat("b", 40)))
	if len(l.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(l.Packages))
	}
	got, ok := l.Find("fmt")
	if !ok {
		t.Fatal("Find failed after Put")
	}
	if got.Commit != strings.Repeat("b", 40) {
		t.Error("Put did not replace the existing entry")
	}
}

func TestRemove(t *testing.T) {
	l := New("0.1.0")
	l.Put(pkg("fmt", strings.Repeat("a", 40)))
	if !l.Remove("fmt") {
		t.Error("Remove reported false for a present package")
	}
	if l.Remove("fmt") {
		t.Error("Remove reported true for an absent package")
	}
	if len(l.Packages) != 0 {
		t.Error("package survived removal")
	}
}

func TestRoundTrip(t *testing.T) {
	l := New("0.1.0")
	l.Put(pkg("fmt", strings.Repeat("a", 40)))
	path := filepath.Join(t.TempDir(), FileName)
	if err := l.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packages) != 1 || got.Packages[0].Commit != strings.Repeat("a", 40) {
		t.Errorf("round trip lost data: %+v", got.Packages)
	}
}

func TestLoadRejectsFutureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := writeFile(path, `{"schema":99,"generated_by":"x","packages":[]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected an unsupported schema version to be rejected")
	}
}
