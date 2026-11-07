// Package sources expands the source patterns in cppm.json into the concrete
// file lists cppm writes into the generated CMake.
//
// The expansion happens here, in Go, at `cppm sync` time, and deliberately not
// in CMake. file(GLOB) at configure time is the obvious alternative and it is a
// trap twice over: without CONFIGURE_DEPENDS it is not re-evaluated, so a new
// file is invisible until something else forces a reconfigure; and with it, the
// set of translation units in a build is whatever happened to be on disk at
// configure time and appears in no diff. Expanding here means the file list is
// written into a generated file that a reviewer can read.
//
// The cost is that creating a source file is not enough on its own -- `cppm
// sync` (or `cppm build`, which syncs first) has to run. That is the trade.
package sources

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Extensions are the C++ source extensions a pattern naming a directory
// expands to.
//
// Not a complete list of things a compiler accepts: .c is absent because a
// directory of C sources in a C++ project is usually vendored code the user
// does not mean to compile into their own target, and headers are absent
// because adding them to a target changes nothing but IDE grouping. Either can
// still be had with an explicit pattern -- "src/**/*.c" is matched verbatim and
// never consults this list.
var Extensions = []string{".cpp", ".cc", ".cxx", ".c++"}

// skipDirName reports whether a directory is cppm's or a tool's rather than the
// user's, and so must never be searched for sources.
//
// .cppm matters most: it holds the full source tree of every dependency, so a
// pattern like "**/*.cpp" would otherwise pull fmt and Catch2 into the user's
// own target. build/ is cppm's default binaryDir, where CMake and FetchContent
// write generated sources.
func skipDirName(rel, name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	return rel == "build"
}

// isBuildTree reports whether dir is a configured CMake build directory.
//
// Catches build trees that are not called "build" -- out-of-source builds in
// cmake-build-debug/ and the like, which are full of CMake's own generated
// .cpp files.
func isBuildTree(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "CMakeCache.txt"))
	return err == nil
}

// Validate rejects a pattern cppm will not expand, naming what to write
// instead. Called from manifest validation, so a hand-edited cppm.json fails
// at load with a usable message rather than at expansion time.
func Validate(pattern string) error {
	switch {
	case pattern == "":
		return fmt.Errorf("empty source pattern")
	case strings.HasPrefix(pattern, "-"):
		return fmt.Errorf("source pattern %q starts with '-'; write \"./%s\" if that is really a path",
			pattern, pattern)
	case strings.Contains(pattern, `\`):
		return fmt.Errorf("source pattern %q uses backslashes; CMake wants forward slashes on every platform, including Windows",
			pattern)
	// A bare colon is a legal character in a POSIX filename, so only a drive
	// prefix ("C:/src") counts as absolute here.
	case path.IsAbs(pattern), filepath.IsAbs(pattern), len(pattern) > 1 && pattern[1] == ':':
		return fmt.Errorf("source pattern %q is absolute; patterns are relative to the directory holding cppm.json",
			pattern)
	}
	for _, seg := range strings.Split(pattern, "/") {
		if seg == ".." {
			return fmt.Errorf("source pattern %q leaves the project; cppm only compiles files inside it", pattern)
		}
	}
	// path.Match reports a malformed pattern only when it reaches the bad
	// syntax, so check each segment against a name it cannot short-circuit on.
	for _, seg := range strings.Split(pattern, "/") {
		if _, err := path.Match(seg, "x"); err != nil {
			return fmt.Errorf("source pattern %q is malformed: %v (an unclosed '[' usually)", pattern, err)
		}
	}
	return nil
}

// Match reports whether the slash-separated path rel matches pattern.
//
// Segment-wise path.Match, with "**" spanning zero or more segments. Within one
// segment the syntax is CMake's and the shell's: '*', '?' and [class].
// path.Match is per-segment precisely so that '*' cannot cross a '/' -- the
// behaviour people expect and the reason filepath.Match is not used directly.
func Match(pattern, rel string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], name[0])
		if err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// hasMeta reports whether pattern contains wildcard syntax.
func hasMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// literalPrefix returns the longest leading run of wildcard-free directory
// segments, so expansion walks src/net rather than the whole project for
// "src/net/**/*.cpp".
func literalPrefix(pattern string) string {
	segs := strings.Split(pattern, "/")
	var out []string
	for _, s := range segs[:max(len(segs)-1, 0)] {
		if hasMeta(s) {
			break
		}
		out = append(out, s)
	}
	return strings.Join(out, "/")
}

// Result is one pattern's expansion. Files is empty when the pattern matched
// nothing, which callers report rather than treat as an error: a pattern for a
// directory that does not exist yet is a typo most of the time but legitimately
// ahead of the files the rest of the time.
type Result struct {
	Pattern string
	Files   []string
	// Dir records that the pattern named a directory and was expanded to every
	// C++ source beneath it, which is worth saying in output because it is the
	// one case where the pattern is not what was matched.
	Dir bool
}

// Expand expands every pattern against root, in the order given.
func Expand(root string, patterns []string) ([]Result, error) {
	out := make([]Result, 0, len(patterns))
	for _, p := range patterns {
		if err := Validate(p); err != nil {
			return nil, err
		}
		r, err := expandOne(root, p)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func expandOne(root, pattern string) (Result, error) {
	res := Result{Pattern: pattern}

	if !hasMeta(pattern) {
		full := filepath.Join(root, filepath.FromSlash(pattern))
		fi, err := os.Stat(full)
		switch {
		case os.IsNotExist(err):
			return res, nil
		case err != nil:
			return res, err
		case fi.IsDir():
			res.Dir = true
			files, err := walk(root, pattern, func(rel string) bool { return isSource(rel) })
			res.Files = files
			return res, err
		default:
			res.Files = []string{pattern}
			return res, nil
		}
	}

	files, err := walk(root, literalPrefix(pattern), func(rel string) bool {
		return Match(pattern, rel)
	})
	res.Files = files
	return res, err
}

// walk collects the files under root/base that keep accepts, as sorted
// slash-separated paths relative to root.
func walk(root, base string, keep func(rel string) bool) ([]string, error) {
	start := root
	if base != "" {
		start = filepath.Join(root, filepath.FromSlash(base))
	}
	if _, err := os.Stat(start); os.IsNotExist(err) {
		return nil, nil
	}

	var out []string
	err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relOS, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel := filepath.ToSlash(relOS)

		if d.IsDir() {
			// The starting directory is exempt: a project may well live in a
			// path with a dot in it, and skipping the root would find nothing.
			if p == start {
				return nil
			}
			if skipDirName(rel, d.Name()) || isBuildTree(p) {
				return fs.SkipDir
			}
			return nil
		}
		// Symlinked files are followed by the compiler, but WalkDir does not
		// descend symlinked directories, so a link to a tree outside the
		// project cannot smuggle files in.
		if keep(rel) {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func isSource(rel string) bool {
	ext := strings.ToLower(path.Ext(rel))
	for _, e := range Extensions {
		if ext == e {
			return true
		}
	}
	return false
}

// Files merges the results into one sorted, deduplicated list, dropping
// anything in exclude.
//
// exclude carries the target's "sources" entries: those are already named in
// the user's own CMakeLists.txt, and listing a file twice in one target is
// noise in the generated file even though CMake tolerates it.
func Files(results []Result, exclude []string) []string {
	seen := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		seen[filepath.ToSlash(e)] = true
	}
	var out []string
	for _, r := range results {
		for _, f := range r.Files {
			if seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}
