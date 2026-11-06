// Package discover works out which CMake target a dependency expects you to
// link.
//
// This exists because CMake will not tell you. Asking a configured project for
// a dependency's targets via BUILDSYSTEM_TARGETS returns only the raw
// underlying targets -- for fmt, "fmt;fmt-header-only" -- while the name users
// are documented to link, fmt::fmt, is an ALIAS, and CMake has no API that
// enumerates aliases. It will however confirm a name you already have, with
// if(TARGET x).
//
// So discovery is: generate candidate names from the source, rank them, and let
// CMake verify the winner. Measured at 8 of 10 real libraries correct with zero
// confident errors; see docs/architecture.md section 3. The two it misses build
// their targets inside helper macros, which no scanner can read, and are
// handled by the recipe database and by configure-time probing.
//
// The guiding rule is that declining to answer beats answering wrongly: a wrong
// target name produces a build that configures and links the wrong thing.
package discover

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Result is the outcome of scanning one dependency's source tree.
type Result struct {
	// Best is the highest-ranked candidate, or empty when discovery declined.
	Best string
	// Candidates are all surviving candidates, best first.
	Candidates []Candidate
	// Rejected are candidates disqualified outright, for `cppm info`.
	Rejected []Candidate
	// ProjectName is the dependency's own project() name, often a better clue
	// than the repository name.
	ProjectName string
	// Confident reports whether Best should be used without asking.
	Confident bool
	// Why explains a refusal in one line.
	Why string
}

// Candidate is a possible link target.
type Candidate struct {
	Name    string
	IsAlias bool
	Kind    string
	Score   int
	Reasons []string
	Veto    string // non-empty if disqualified
	// File is the CMake file that defines the target, relative to the source
	// root. Needed to tell a target the build really creates from one that only
	// a packaging module defines; see reachableFrom.
	File string
}

var (
	reAlias = regexp.MustCompile(`(?is)\badd_library\s*\(\s*([A-Za-z0-9_:.${}-]+)\s+ALIAS\s+([A-Za-z0-9_.${}-]+)\s*\)`)
	reReal  = regexp.MustCompile(`(?is)\badd_library\s*\(\s*([A-Za-z0-9_:.${}-]+)\s+(INTERFACE|STATIC|SHARED|MODULE|OBJECT)\b`)
	reProj  = regexp.MustCompile(`(?is)\bproject\s*\(\s*([A-Za-z0-9_.-]+)`)
	reSet   = regexp.MustCompile(`(?is)\bset\s*\(\s*([A-Za-z0-9_]+)\s+([^)]*)\)`)
	reVar   = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}`)

	// The two ways a CMake file pulls in another. The \s*\( guard keeps
	// include_directories() and include_guard() out of reInclude.
	reSubdir  = regexp.MustCompile(`(?is)\badd_subdirectory\s*\(\s*("[^"]+"|[^\s)]+)`)
	reInclude = regexp.MustCompile(`(?is)\binclude\s*\(\s*("[^"]+"|[^\s)]+)`)
)

// skipDirs hold targets that are never a library's public interface.
var skipDirs = []string{
	"test/", "tests/", "example/", "examples/", "benchmark/", "benchmarks/",
	"third_party/", "thirdparty/", "extern/", "external/", "vendor/",
	"doc/", "docs/", "fuzz/", "fuzzing/", "sample/", "samples/",
}

// vetoMarkers disqualify a candidate outright rather than merely penalising it.
//
// A soft penalty was not enough: abseil ranked absl::abseil_test_dll first and
// recommended it with confidence. The marker is ignored when it also appears in
// the package's own name, so a library called googletest is not vetoed by itself.
var vetoMarkers = []string{
	"test", "benchmark", "bench", "example", "sample", "mock", "fuzz", "dll",
	"_dummy", "buildall",
}

// demoteMarkers can legitimately be the answer (Catch2::Catch2WithMain) but are
// less likely than the plain form.
var demoteMarkers = []string{
	"header-only", "header_only", "headeronly",
	"-static", "_static", "-shared", "_shared",
	"main", "tool", "util", "internal", "experimental",
}

// Scan inspects a checked-out source tree and ranks its link targets.
//
// pkgName is the dependency's name as the user refers to it; the dependency's
// own project() name is discovered and used as a second alias, which is what
// makes nlohmann_json::nlohmann_json reachable from a dependency called "json".
func Scan(root, pkgName string) (Result, error) {
	files, err := readCMakeFiles(root)
	if err != nil {
		return Result{}, err
	}

	vars, projectName := collectVars(files)
	raw := extractTargets(files, vars)
	reachable := reachableFrom(files, vars)

	aliases := []string{pkgName}
	if projectName != "" && !strings.EqualFold(projectName, pkgName) {
		aliases = append(aliases, projectName)
	}

	var live, dead []Candidate
	for _, c := range raw {
		if marker, bad := vetoed(c.Name, aliases); bad {
			c.Veto = marker
			dead = append(dead, c)
			continue
		}
		live = append(live, c)
	}
	score(live, aliases)
	penalizeUnreachable(live, reachable)
	sortCandidates(live)
	sortCandidates(dead)

	res := Result{Candidates: live, Rejected: dead, ProjectName: projectName}
	switch {
	case len(live) == 0 && len(dead) > 0:
		res.Why = "every candidate looked like test or example scaffolding"
	case len(live) == 0:
		res.Why = "no add_library() calls with literal names; targets are probably built by helper macros"
	case !related(live[0].Name, aliases):
		res.Best = live[0].Name
		res.Why = "the best candidate does not look like it belongs to this package"
	default:
		res.Best = live[0].Name
		res.Confident = true
	}
	return res, nil
}

// Ambiguous reports whether the top candidates are close enough that the user
// should choose.
//
// Catch2::Catch2 versus Catch2::Catch2WithMain is a decision, not an unknown.
func (r Result) Ambiguous() bool {
	if len(r.Candidates) < 2 {
		return false
	}
	return r.Candidates[0].Score-r.Candidates[1].Score < 20
}

// TopNames returns up to n candidate names, best first.
func (r Result) TopNames(n int) []string {
	out := make([]string, 0, n)
	for i, c := range r.Candidates {
		if i >= n {
			break
		}
		out = append(out, c.Name)
	}
	return out
}

type cmakeFile struct {
	rel  string
	text string
}

func readCMakeFiles(root string) ([]cmakeFile, error) {
	var files []cmakeFile
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // an unreadable entry must not abort discovery
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		base := info.Name()
		if base != "CMakeLists.txt" && !strings.HasSuffix(base, ".cmake") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || skipDir(rel) {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil || len(b) > 4<<20 {
			return nil
		}
		files = append(files, cmakeFile{filepath.ToSlash(rel), string(b)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Shallowest first, so a top-level definition wins over a nested one.
	sort.SliceStable(files, func(i, j int) bool {
		return strings.Count(files[i].rel, "/") < strings.Count(files[j].rel, "/")
	})
	return files, nil
}

func skipDir(rel string) bool {
	p := strings.ToLower(filepath.ToSlash(rel))
	for _, bad := range skipDirs {
		if strings.HasPrefix(p, bad) || strings.Contains(p, "/"+bad) {
			return true
		}
	}
	return false
}

// unreachablePenalty has to outweigh the namespaced-alias bonus, because that
// bonus is what put an unreachable target on top in the first place.
const unreachablePenalty = 60

// reachableFrom returns the CMake files the top-level CMakeLists.txt can
// actually reach, following add_subdirectory() and include().
//
// Without this, duckdb is discovered confidently and wrongly. Its only
// namespaced alias, duckdb::cpp_api, is defined in
// tools/cpp/cmake/DuckDBCppApi.cmake -- a module for the packaged release,
// included only from tools/cpp/package/, which nothing adds as a subdirectory.
// The target does not exist in a FetchContent build, so recommending it
// produces a configure error from cppm's own target assertion. A scanner that
// reads every *.cmake in the tree cannot tell that file from a real one.
//
// Conditions are ignored, so the result is a superset of what any single
// configuration defines, and resolution is deliberately forgiving: an edge this
// walk fails to follow only costs a real target its ranking, which makes cppm
// ask instead of answer. Returns nil when there is no top-level CMakeLists.txt
// to start from, which means "unknown" rather than "nothing is reachable".
func reachableFrom(files []cmakeFile, vars map[string]string) map[string]bool {
	byRel := make(map[string]int, len(files))
	byBase := map[string][]int{}
	for i, f := range files {
		byRel[strings.ToLower(f.rel)] = i
		b := strings.ToLower(path.Base(f.rel))
		byBase[b] = append(byBase[b], i)
	}
	start, ok := byRel["cmakelists.txt"]
	if !ok {
		return nil
	}

	resolve := func(fromDir, arg string, subdir bool) []int {
		arg = expand(strings.Trim(strings.TrimSpace(arg), `"`), vars)
		// A ${CMAKE_CURRENT_LIST_DIR}-style prefix does not expand here, but the
		// literal tail after it is still a usable path, so keep that rather than
		// discarding the edge.
		if i := strings.LastIndex(arg, "}"); i >= 0 {
			arg = arg[i+1:]
		}
		arg = strings.Trim(strings.TrimPrefix(filepath.ToSlash(arg), "./"), "/")
		if arg == "" || strings.Contains(arg, "$") {
			return nil
		}
		want := arg
		switch {
		case subdir:
			want = path.Join(arg, "CMakeLists.txt")
		case !strings.HasSuffix(strings.ToLower(want), ".cmake"):
			want += ".cmake"
		}
		var out []int
		for _, c := range []string{path.Join(fromDir, want), want} {
			if i, ok := byRel[strings.ToLower(c)]; ok {
				out = append(out, i)
			}
		}
		if len(out) > 0 {
			return out
		}
		// CMAKE_MODULE_PATH lets include() name a module living anywhere in the
		// tree, and the same applies to a subdirectory reached through a path
		// this walk could not expand: fall back to matching the file name.
		if !subdir {
			return byBase[strings.ToLower(path.Base(want))]
		}
		suffix := "/" + strings.ToLower(want)
		for i, f := range files {
			if strings.HasSuffix(strings.ToLower(f.rel), suffix) {
				out = append(out, i)
			}
		}
		return out
	}

	seen := map[int]bool{start: true}
	for queue := []int{start}; len(queue) > 0; {
		cur := queue[0]
		queue = queue[1:]
		dir := path.Dir(files[cur].rel)
		if dir == "." {
			dir = ""
		}
		for _, edges := range []struct {
			re     *regexp.Regexp
			subdir bool
		}{{reSubdir, true}, {reInclude, false}} {
			for _, m := range edges.re.FindAllStringSubmatch(files[cur].text, -1) {
				for _, i := range resolve(dir, m[1], edges.subdir) {
					if !seen[i] {
						seen[i] = true
						queue = append(queue, i)
					}
				}
			}
		}
	}

	out := make(map[string]bool, len(seen))
	for i := range seen {
		out[files[i].rel] = true
	}
	return out
}

// penalizeUnreachable demotes targets that only an unreachable CMake file
// defines. It demotes rather than drops them: the walk in reachableFrom is
// approximate, and a target wrongly judged unreachable should lose the argument
// on score, not be hidden from the user.
func penalizeUnreachable(cands []Candidate, reachable map[string]bool) {
	if reachable == nil {
		return
	}
	// No candidate reachable at all means the walk failed rather than that every
	// target is unusable. Penalising the whole set would only add noise.
	anyReachable := false
	for i := range cands {
		if reachable[cands[i].File] {
			anyReachable = true
			break
		}
	}
	if !anyReachable {
		return
	}
	for i := range cands {
		c := &cands[i]
		if c.File == "" || reachable[c.File] {
			continue
		}
		c.Score -= unreachablePenalty
		c.Reasons = append(c.Reasons, "defined in a CMake file the build never reaches")
	}
}

// collectVars gathers project() and single-valued set() definitions.
//
// Not optional. Target names are routinely assembled from variables -- json
// declares add_library(${PROJECT_NAME}::${NLOHMANN_JSON_TARGET_NAME} ...) -- and
// adding this step moved the measured accuracy from 60% to 80%.
func collectVars(files []cmakeFile) (map[string]string, string) {
	var projectName string
	for _, f := range files {
		if f.rel == "CMakeLists.txt" {
			if m := reProj.FindStringSubmatch(f.text); m != nil {
				projectName = m[1]
			}
		}
	}
	vars := map[string]string{
		"PROJECT_NAME":       projectName,
		"CMAKE_PROJECT_NAME": projectName,
	}
	for _, f := range files {
		for _, m := range reSet.FindAllStringSubmatch(f.text, -1) {
			name := m[1]
			fields := strings.Fields(strings.TrimSpace(m[2]))
			if len(fields) == 0 {
				continue
			}
			v := strings.Trim(fields[0], `"'`)
			if v == "" || strings.EqualFold(v, "CACHE") {
				continue
			}
			if _, exists := vars[name]; !exists {
				vars[name] = v
			}
		}
	}
	for k, v := range vars {
		vars[k] = expand(v, vars)
	}
	return vars, projectName
}

func expand(s string, vars map[string]string) string {
	for i := 0; i < 6 && strings.Contains(s, "${"); i++ {
		s = reVar.ReplaceAllStringFunc(s, func(m string) string {
			if v, ok := vars[m[2:len(m)-1]]; ok && v != "" {
				return v
			}
			return m
		})
	}
	return s
}

func extractTargets(files []cmakeFile, vars map[string]string) []Candidate {
	seen := map[string]*Candidate{}
	add := func(name, kind, file string, isAlias bool) {
		name = expand(strings.TrimSpace(name), vars)
		// Still unresolved after expansion: not usable as a link name.
		if name == "" || strings.Contains(name, "$") {
			return
		}
		if c, ok := seen[name]; ok {
			if isAlias {
				c.IsAlias, c.Kind = true, "ALIAS"
			}
			return
		}
		seen[name] = &Candidate{Name: name, IsAlias: isAlias, Kind: kind, File: file}
	}
	for _, f := range files {
		for _, m := range reAlias.FindAllStringSubmatch(f.text, -1) {
			add(m[1], "ALIAS", f.rel, true)
		}
		for _, m := range reReal.FindAllStringSubmatch(f.text, -1) {
			add(m[1], strings.ToUpper(m[2]), f.rel, false)
		}
	}
	out := make([]Candidate, 0, len(seen))
	for _, c := range seen {
		out = append(out, *c)
	}
	return out
}

func splitName(name string) (ns, bare string) {
	if i := strings.Index(name, "::"); i >= 0 {
		return name[:i], name[i+2:]
	}
	return "", name
}

func vetoed(name string, aliases []string) (string, bool) {
	_, bare := splitName(strings.ToLower(name))
	for _, m := range vetoMarkers {
		if !strings.Contains(bare, m) {
			continue
		}
		inPkg := false
		for _, a := range aliases {
			if strings.Contains(strings.ToLower(a), m) {
				inPkg = true
				break
			}
		}
		if !inPkg {
			return m, true
		}
	}
	return "", false
}

func score(cands []Candidate, aliases []string) {
	for i := range cands {
		c := &cands[i]
		ns, bare := splitName(c.Name)
		lbare, lns := strings.ToLower(bare), strings.ToLower(ns)

		if ns != "" {
			c.Score += 50
			c.Reasons = append(c.Reasons, "namespaced")
		}
		if c.IsAlias {
			c.Score += 20
			c.Reasons = append(c.Reasons, "alias")
		}

		best := 0
		for _, a := range aliases {
			la := strings.ToLower(a)
			if la == "" {
				continue
			}
			switch {
			case lbare == la && best < 40:
				best = 40
			case (strings.HasPrefix(lbare, la) || strings.HasPrefix(la, lbare)) && best < 20:
				best = 20
			}
		}
		if best > 0 {
			c.Score += best
			c.Reasons = append(c.Reasons, "name matches package")
		}
		for _, a := range aliases {
			la := strings.ToLower(a)
			if lns != "" && la != "" && (lns == la || strings.HasPrefix(lns, la) || strings.HasPrefix(la, lns)) {
				c.Score += 25
				c.Reasons = append(c.Reasons, "namespace matches package")
				break
			}
		}
		if lns != "" && lns == lbare {
			c.Score += 15
			c.Reasons = append(c.Reasons, "canonical ns::ns form")
		}
		for _, d := range demoteMarkers {
			if strings.Contains(lbare, d) {
				c.Score -= 30
				c.Reasons = append(c.Reasons, "variant: "+d)
				break
			}
		}
		if c.Kind == "INTERFACE" {
			c.Score += 5
		}
		c.Score -= len(bare) / 8
	}
}

func sortCandidates(cands []Candidate) {
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		return cands[i].Name < cands[j].Name
	})
}

// related reports whether a name plausibly belongs to this package.
//
// The confidence gate: an unrelated top pick is worse than no answer.
func related(name string, aliases []string) bool {
	ns, bare := splitName(strings.ToLower(name))
	for _, a := range aliases {
		a = strings.ToLower(a)
		if a == "" {
			continue
		}
		for _, part := range []string{ns, bare} {
			if part == "" {
				continue
			}
			if part == a || strings.HasPrefix(part, a) || strings.HasPrefix(a, part) {
				return true
			}
			if commonPrefix(part, a) >= 4 {
				return true
			}
		}
	}
	return false
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
