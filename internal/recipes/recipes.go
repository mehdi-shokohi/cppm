// Package recipes holds curated knowledge about common C++ libraries.
//
// Two things cannot be discovered by reading a dependency's source:
//
//   - Target names built inside CMake helper macros. googletest declares
//     add_library(${cmake_package_name}::${name} ALIAS ${name}) where both
//     halves are function parameters, so GTest::gtest_main exists only while
//     CMake is running.
//   - Cache options with no observable signal at all. spdlog silently compiles
//     its own bundled copy of fmt unless SPDLOG_FMT_EXTERNAL=ON, and nothing in
//     its metadata says so.
//
// The database is small on purpose. It is a shortcut for libraries people
// actually use, not a package registry -- discovery handles the long tail.
package recipes

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed recipes.json
var builtin embed.FS

// Recipe is curated knowledge for one library.
type Recipe struct {
	// Targets are the link targets to use. An empty list is meaningful: it
	// records that the library exports nothing usable, with Notes saying so.
	Targets []string `json:"targets"`
	// Alternatives are other valid choices, offered when the user is asked.
	Alternatives []string `json:"alternatives,omitempty"`
	// Options are cache variables to set before the dependency is made
	// available.
	Options map[string]string `json:"options,omitempty"`
	// Notes is shown to the user; it explains anything surprising.
	Notes string `json:"notes,omitempty"`
}

type file struct {
	Comment string            `json:"comment"`
	Recipes map[string]Recipe `json:"recipes"`
}

var loaded map[string]Recipe

// All returns every known recipe, keyed by canonical repository URL.
//
// User recipes from $CPPM_RECIPES_DIR or the user config directory override the
// embedded ones, so a wrong or outdated entry can be fixed without waiting for
// a cppm release.
func All() map[string]Recipe {
	if loaded != nil {
		return loaded
	}
	loaded = map[string]Recipe{}

	b, err := builtin.ReadFile("recipes.json")
	if err == nil {
		var f file
		if err := json.Unmarshal(b, &f); err == nil {
			for k, v := range f.Recipes {
				loaded[normalizeURL(k)] = v
			}
		}
	}
	for k, v := range userRecipes() {
		loaded[normalizeURL(k)] = v
	}
	return loaded
}

// userRecipes loads every *.json in the user's recipe directory.
func userRecipes() map[string]Recipe {
	out := map[string]Recipe{}
	dir := os.Getenv("CPPM_RECIPES_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return out
		}
		dir = filepath.Join(base, "cppm", "recipes")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var f file
		if err := json.Unmarshal(b, &f); err != nil {
			continue
		}
		for k, v := range f.Recipes {
			out[k] = v
		}
	}
	return out
}

// Lookup finds a recipe for a repository URL.
func Lookup(repoURL string) (Recipe, bool) {
	r, ok := All()[normalizeURL(repoURL)]
	return r, ok
}

// normalizeURL makes URL comparison insensitive to the differences that do not
// change which repository is meant: scheme, credentials, a trailing ".git", a
// trailing slash, and case.
//
// Because it also strips a "#fragment", two keys differing only by fragment
// collapse to the same entry and one silently wins on map iteration order,
// making resolution nondeterministic. Validate rejects that, and
// normalizedKeys exists so it can.
func normalizeURL(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	// A "#suffix" marks a variant entry rather than part of the URL.
	if i := strings.Index(s, "#"); i >= 0 {
		s = s[:i]
	}
	for _, prefix := range []string{"https://", "http://", "ssh://", "git://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	if i := strings.Index(s, "@"); i >= 0 && i < strings.Index(s+"/", "/") {
		s = s[i+1:]
	}
	s = strings.Replace(s, ":", "/", 1)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}

// Names returns the known repository keys, sorted, for `cppm recipes`.
func Names() []string {
	all := All()
	out := make([]string, 0, len(all))
	for k := range all {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// normalizedKeys maps each normalized key to the raw keys that produced it.
func normalizedKeys() map[string][]string {
	out := map[string][]string{}
	b, err := builtin.ReadFile("recipes.json")
	if err != nil {
		return out
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return out
	}
	for raw := range f.Recipes {
		n := normalizeURL(raw)
		out[n] = append(out[n], raw)
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}

// Validate checks the embedded database at build time via the test suite.
func Validate() error {
	// Two keys normalizing to the same URL make lookup depend on map iteration
	// order, so the same command can resolve to different targets on different
	// runs. That has happened once already.
	for norm, raws := range normalizedKeys() {
		if len(raws) > 1 {
			return fmt.Errorf("recipe keys %v all normalize to %q, so lookup would be nondeterministic", raws, norm)
		}
	}
	for url, r := range All() {
		if url == "" {
			return fmt.Errorf("recipe with an empty URL key")
		}
		if len(r.Targets) == 0 && r.Notes == "" {
			return fmt.Errorf("%s: no targets and no note explaining why", url)
		}
		for k := range r.Options {
			if k == "" {
				return fmt.Errorf("%s: empty option name", url)
			}
		}
	}
	return nil
}
