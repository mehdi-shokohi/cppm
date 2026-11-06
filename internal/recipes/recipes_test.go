package recipes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedDatabaseIsValid(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	if len(All()) < 15 {
		t.Errorf("expected a meaningful recipe set, got %d", len(All()))
	}
}

// These are the entries that justify the database existing at all: their
// targets cannot be recovered by reading the source.
func TestCoversTheLibrariesDiscoveryCannotHandle(t *testing.T) {
	for _, tc := range []struct {
		url    string
		target string
	}{
		{"https://github.com/google/googletest.git", "GTest::gtest_main"},
		{"https://github.com/abseil/abseil-cpp.git", "absl::base"},
	} {
		r, ok := Lookup(tc.url)
		if !ok {
			t.Errorf("%s has no recipe, but its targets are built by helper macros", tc.url)
			continue
		}
		if len(r.Targets) == 0 || r.Targets[0] != tc.target {
			t.Errorf("%s: targets = %v, want %s first", tc.url, r.Targets, tc.target)
		}
	}
}

// Option knowledge that no probe can ever discover.
func TestCarriesUndiscoverableOptions(t *testing.T) {
	absl, ok := Lookup("https://github.com/abseil/abseil-cpp.git")
	if !ok {
		t.Fatal("no abseil recipe")
	}
	if absl.Options["ABSL_PROPAGATE_CXX_STD"] != "ON" {
		t.Error("abseil needs ABSL_PROPAGATE_CXX_STD=ON or it builds against a different standard")
	}
	spdlog, ok := Lookup("https://github.com/gabime/spdlog.git")
	if !ok {
		t.Fatal("no spdlog recipe")
	}
	if spdlog.Notes == "" {
		t.Error("spdlog bundles its own fmt unless told otherwise; that must be noted")
	}
}

// A recipe with no targets is a real answer -- the library exports none -- so it
// must say why.
func TestEmptyTargetListIsExplained(t *testing.T) {
	for url, r := range All() {
		if len(r.Targets) == 0 && r.Notes == "" {
			t.Errorf("%s: no targets and no explanation", url)
		}
	}
}

// URL spelling must not decide whether a recipe is found.
func TestLookupIsInsensitiveToURLSpelling(t *testing.T) {
	for _, u := range []string{
		"https://github.com/fmtlib/fmt.git",
		"https://github.com/fmtlib/fmt",
		"http://github.com/fmtlib/fmt.git",
		"https://GitHub.com/FmtLib/fmt.git",
		"git@github.com:fmtlib/fmt.git",
		"ssh://git@github.com/fmtlib/fmt.git",
		"https://github.com/fmtlib/fmt/",
	} {
		if _, ok := Lookup(u); !ok {
			t.Errorf("Lookup(%q) missed the fmt recipe", u)
		}
	}
	if _, ok := Lookup("https://github.com/someone/unknown-library.git"); ok {
		t.Error("an unknown library must not match a recipe")
	}
}

// A wrong or outdated embedded entry must be fixable without a cppm release.
func TestUserRecipesOverrideEmbedded(t *testing.T) {
	dir := t.TempDir()
	custom := `{"recipes":{"https://github.com/fmtlib/fmt.git":{"targets":["my::override"]}}}`
	if err := os.WriteFile(filepath.Join(dir, "mine.json"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPPM_RECIPES_DIR", dir)
	loaded = nil // force a reload
	defer func() { loaded = nil }()

	r, ok := Lookup("https://github.com/fmtlib/fmt.git")
	if !ok {
		t.Fatal("fmt recipe disappeared")
	}
	if len(r.Targets) != 1 || r.Targets[0] != "my::override" {
		t.Errorf("user recipe did not override the embedded one: %v", r.Targets)
	}
}

func TestMalformedUserRecipesAreIgnoredNotFatal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPPM_RECIPES_DIR", dir)
	loaded = nil
	defer func() { loaded = nil }()

	if _, ok := Lookup("https://github.com/fmtlib/fmt.git"); !ok {
		t.Error("a malformed user recipe file must not break the embedded database")
	}
}

// Two recipe keys that normalize to the same URL make lookup depend on Go's
// random map iteration order, so the same `cppm add` resolves to different
// targets on different runs. This happened with a "Catch2.git#v2" variant key.
func TestNoCollidingRecipeKeys(t *testing.T) {
	for norm, raws := range normalizedKeys() {
		if len(raws) > 1 {
			t.Errorf("keys %v all normalize to %q: lookup would be nondeterministic", raws, norm)
		}
	}
}

func TestLookupIsDeterministic(t *testing.T) {
	const url = "https://github.com/catchorg/Catch2.git"
	loaded = nil
	first, ok := Lookup(url)
	if !ok {
		t.Fatal("no Catch2 recipe")
	}
	for i := 0; i < 50; i++ {
		loaded = nil
		again, ok := Lookup(url)
		if !ok {
			t.Fatalf("run %d: recipe disappeared", i)
		}
		if len(again.Targets) != len(first.Targets) {
			t.Fatalf("run %d: target count changed", i)
		}
		for j := range again.Targets {
			if again.Targets[j] != first.Targets[j] {
				t.Fatalf("run %d: resolved to %v, first run gave %v", i, again.Targets, first.Targets)
			}
		}
	}
	loaded = nil
}
