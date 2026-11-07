package sources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchSegmentWise(t *testing.T) {
	cases := []struct {
		pattern, rel string
		want         bool
	}{
		{"src/main.cpp", "src/main.cpp", true},
		{"src/main.cpp", "src/other.cpp", false},

		// '*' must not cross a separator: this is the whole reason Match is
		// segment-wise rather than a call to filepath.Match.
		{"src/*.cpp", "src/a.cpp", true},
		{"src/*.cpp", "src/net/a.cpp", false},

		{"src/**/*.cpp", "src/a.cpp", true},
		{"src/**/*.cpp", "src/net/a.cpp", true},
		{"src/**/*.cpp", "src/net/tcp/a.cpp", true},
		{"src/**/*.cpp", "other/a.cpp", false},
		{"src/**/*.cpp", "src/a.hpp", false},

		{"**/*.cpp", "a.cpp", true},
		{"**/*.cpp", "deep/down/a.cpp", true},
		{"src/**", "src/net/a.cpp", true},
		{"src/**", "src", true},

		{"src/?.cpp", "src/a.cpp", true},
		{"src/?.cpp", "src/ab.cpp", false},
		{"src/[ab].cpp", "src/b.cpp", true},
		{"src/[ab].cpp", "src/c.cpp", false},

		// A directory pattern does not match the files under it by itself;
		// expandOne turns it into a walk instead.
		{"src/net", "src/net/a.cpp", false},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.rel); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.rel, got, c.want)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct{ pattern, mustMention string }{
		{"", "empty"},
		{"/abs/src/*.cpp", "absolute"},
		{"../sibling/*.cpp", "leaves the project"},
		{"src/../../etc/*.cpp", "leaves the project"},
		{`src\net\*.cpp`, "backslash"},
		{"-rf", "starts with '-'"},
		{"src/[a.cpp", "malformed"},
	}
	for _, c := range cases {
		err := Validate(c.pattern)
		if err == nil {
			t.Errorf("Validate(%q) = nil, want an error", c.pattern)
			continue
		}
		if !strings.Contains(err.Error(), c.mustMention) {
			t.Errorf("Validate(%q) = %q, want it to mention %q", c.pattern, err, c.mustMention)
		}
	}
	for _, ok := range []string{"src", "src/net", "src/*.cpp", "src/**/*.cpp", "a.cpp"} {
		if err := Validate(ok); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
	}
}

// write creates root/rel with a one-line body, making parents as needed.
func write(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("// x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func files(t *testing.T, root string, patterns ...string) []string {
	t.Helper()
	res, err := Expand(root, patterns)
	if err != nil {
		t.Fatal(err)
	}
	return Files(res, nil)
}

func TestExpandDirectoryTakesEveryCppSource(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/main.cpp")
	write(t, root, "src/net/socket.cpp")
	write(t, root, "src/net/legacy.cc")
	write(t, root, "src/net/odd.cxx")
	write(t, root, "src/net/socket.hpp") // a header is not a translation unit
	write(t, root, "src/net/notes.md")
	write(t, root, "src/vendor/thing.c") // .c needs an explicit pattern

	got := files(t, root, "src/net")
	want := []string{"src/net/legacy.cc", "src/net/odd.cxx", "src/net/socket.cpp"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}

	// An explicit pattern bypasses the extension list entirely.
	if got := files(t, root, "src/vendor/*.c"); strings.Join(got, ",") != "src/vendor/thing.c" {
		t.Errorf("explicit .c pattern: got %v", got)
	}
}

// The hazard that matters most: .cppm holds every dependency's full source
// tree, so a careless "**/*.cpp" would compile fmt and Catch2 into the user's
// own target. build/ holds CMake's and FetchContent's generated sources.
func TestExpandNeverDescendsIntoGeneratedOrDependencyTrees(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/main.cpp")
	write(t, root, ".cppm/sources/fmt/src/format.cpp")
	write(t, root, ".git/hooks/thing.cpp")
	write(t, root, "build/debug/_deps/catch2-src/src/catch.cpp")
	write(t, root, "cmake-build-debug/CMakeCache.txt")
	write(t, root, "cmake-build-debug/CMakeFiles/feature.cpp")

	got := files(t, root, "**/*.cpp")
	if strings.Join(got, ",") != "src/main.cpp" {
		t.Errorf("got %v, want only src/main.cpp", got)
	}
}

func TestExpandIsSortedAndDeduplicated(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/b.cpp")
	write(t, root, "src/a.cpp")

	// Two patterns matching the same file must not list it twice: the generated
	// CMake is committed-adjacent output and a duplicate is noise.
	got := files(t, root, "src/*.cpp", "src", "src/a.cpp")
	if strings.Join(got, ",") != "src/a.cpp,src/b.cpp" {
		t.Errorf("got %v, want src/a.cpp,src/b.cpp", got)
	}
}

func TestFilesExcludesWhatCMakeListsAlreadyNames(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/main.cpp")
	write(t, root, "src/util.cpp")

	res, err := Expand(root, []string{"src"})
	if err != nil {
		t.Fatal(err)
	}
	got := Files(res, []string{"src/main.cpp"})
	if strings.Join(got, ",") != "src/util.cpp" {
		t.Errorf("got %v, want src/util.cpp only", got)
	}
}

func TestExpandReportsAPatternThatMatchedNothing(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/main.cpp")

	res, err := Expand(root, []string{"src/nett/*.cpp", "does/not/exist"})
	if err != nil {
		t.Fatalf("a pattern matching nothing is not an error: %v", err)
	}
	for _, r := range res {
		if len(r.Files) != 0 {
			t.Errorf("%q matched %v, want nothing", r.Pattern, r.Files)
		}
	}
}

func TestExpandMarksDirectoryPatterns(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/a.cpp")

	res, err := Expand(root, []string{"src", "src/*.cpp", "src/a.cpp"})
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Dir {
		t.Error("a pattern naming a directory must be marked Dir")
	}
	if res[1].Dir || res[2].Dir {
		t.Error("a wildcard or file pattern must not be marked Dir")
	}
}

// A symlinked directory is not followed. Otherwise a link anywhere in the tree
// could pull in files from outside the project, which Validate's ".." check
// exists to prevent.
func TestExpandDoesNotFollowSymlinkedDirectories(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write(t, outside, "secret.cpp")
	write(t, root, "src/main.cpp")

	if err := os.Symlink(outside, filepath.Join(root, "src", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := files(t, root, "src")
	if strings.Join(got, ",") != "src/main.cpp" {
		t.Errorf("got %v, want only src/main.cpp", got)
	}
}
