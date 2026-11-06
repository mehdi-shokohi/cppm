// Command phase0 measures how often cppm can infer the correct CMake link target
// for a real library using only a static source scan, with no curated recipe.
//
// Rationale: CMake cannot enumerate ALIAS targets, which are exactly the names users
// are supposed to link (fmt::fmt, spdlog::spdlog). BUILDSYSTEM_TARGETS returns only
// the raw underlying targets, so "inspect the dependency and read its targets" does
// not work. Namespaced names are however recoverable by scanning the source.
//
// The scan/rank/confidence logic here is the prototype of internal/discover.
//
//	go run ./tools/phase0
//	go run ./tools/phase0 -keep   # reuse existing clones
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------- test corpus

type library struct {
	Name     string
	URL      string
	Tag      string
	Expected []string // any of these counts as correct
	Note     string
}

var corpus = []library{
	{"fmt", "https://github.com/fmtlib/fmt.git", "11.1.4",
		[]string{"fmt::fmt"}, ""},
	{"spdlog", "https://github.com/gabime/spdlog.git", "v1.15.3",
		[]string{"spdlog::spdlog"}, ""},
	{"json", "https://github.com/nlohmann/json.git", "v3.11.3",
		[]string{"nlohmann_json::nlohmann_json"}, "target name built from variables"},
	{"Catch2", "https://github.com/catchorg/Catch2.git", "v3.7.1",
		[]string{"Catch2::Catch2WithMain", "Catch2::Catch2"}, "two legitimate answers"},
	{"googletest", "https://github.com/google/googletest.git", "v1.15.2",
		[]string{"GTest::gtest_main", "GTest::gtest"}, "targets made by helper macros"},
	{"eigen", "https://gitlab.com/libeigen/eigen.git", "3.4.0",
		[]string{"Eigen3::Eigen"}, "non-GitHub host"},
	{"abseil", "https://github.com/abseil/abseil-cpp.git", "20240722.0",
		[]string{"absl::base", "absl::strings"}, "helper functions, no single entry point"},
	{"SDL2", "https://github.com/libsdl-org/SDL.git", "release-2.30.9",
		[]string{"SDL2::SDL2"}, "release-* tag form"},
	{"cxxopts", "https://github.com/jarro2783/cxxopts.git", "v3.2.0",
		[]string{"cxxopts::cxxopts"}, ""},
	{"tomlplusplus", "https://github.com/marzer/tomlplusplus.git", "v3.4.0",
		[]string{"tomlplusplus::tomlplusplus"}, ""},
}

// ---------------------------------------------------------------- source scan

var (
	reAlias = regexp.MustCompile(`(?is)\badd_library\s*\(\s*([A-Za-z0-9_:.${}-]+)\s+ALIAS\s+([A-Za-z0-9_.${}-]+)\s*\)`)
	reReal  = regexp.MustCompile(`(?is)\badd_library\s*\(\s*([A-Za-z0-9_:.${}-]+)\s+(INTERFACE|STATIC|SHARED|MODULE|OBJECT)\b`)
	reProj  = regexp.MustCompile(`(?is)\bproject\s*\(\s*([A-Za-z0-9_.-]+)`)
	reSet   = regexp.MustCompile(`(?is)\bset\s*\(\s*([A-Za-z0-9_]+)\s+([^)]*)\)`)
	reVar   = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}`)
)

// skipDir filters out directories whose targets are never the public API.
func skipDir(rel string) bool {
	p := strings.ToLower(filepath.ToSlash(rel))
	for _, bad := range []string{
		"test/", "tests/", "example/", "examples/", "benchmark/", "benchmarks/",
		"third_party/", "thirdparty/", "extern/", "external/", "vendor/",
		"doc/", "docs/", "fuzz/", "fuzzing/", "sample/", "samples/",
	} {
		if strings.HasPrefix(p, bad) || strings.Contains(p, "/"+bad) {
			return true
		}
	}
	return false
}

type candidate struct {
	Name    string
	IsAlias bool
	Kind    string
	Score   int
	Why     []string
}

type cmakeFile struct{ rel, text string }

// expand substitutes ${VAR} references using a bounded number of passes.
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

// scan walks a cloned source tree and extracts every declared library target name.
// Pass 1 collects project() and set() variables; pass 2 extracts and resolves targets.
func scan(root string) ([]candidate, string, error) {
	var files []cmakeFile

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // unreadable entries are not fatal for a measurement
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
		rel, _ := filepath.Rel(root, path)
		if skipDir(rel) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || len(b) > 4<<20 {
			return nil
		}
		files = append(files, cmakeFile{filepath.ToSlash(rel), string(b)})
		return nil
	})
	if err != nil {
		return nil, "", err
	}

	// ---- pass 1: variables, top-level CMakeLists.txt first ----
	sort.SliceStable(files, func(i, j int) bool {
		return depth(files[i].rel) < depth(files[j].rel)
	})

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
			fs := strings.Fields(strings.TrimSpace(m[2]))
			if len(fs) == 0 {
				continue
			}
			v := strings.Trim(fs[0], `"'`)
			if v == "" || strings.EqualFold(v, "CACHE") {
				continue
			}
			if _, exists := vars[name]; !exists { // shallowest definition wins
				vars[name] = v
			}
		}
	}
	for k, v := range vars {
		vars[k] = expand(v, vars)
	}

	// ---- pass 2: targets ----
	seen := map[string]*candidate{}
	add := func(name, kind string, isAlias bool) {
		name = expand(strings.TrimSpace(name), vars)
		// Still unresolved: cannot be used as a link name.
		if name == "" || strings.Contains(name, "$") {
			return
		}
		if c, ok := seen[name]; ok {
			if isAlias {
				c.IsAlias, c.Kind = true, "ALIAS"
			}
			return
		}
		seen[name] = &candidate{Name: name, IsAlias: isAlias, Kind: kind}
	}
	for _, f := range files {
		for _, m := range reAlias.FindAllStringSubmatch(f.text, -1) {
			add(m[1], "ALIAS", true)
		}
		for _, m := range reReal.FindAllStringSubmatch(f.text, -1) {
			add(m[1], strings.ToUpper(m[2]), false)
		}
	}

	out := make([]candidate, 0, len(seen))
	for _, c := range seen {
		out = append(out, *c)
	}
	return out, projectName, nil
}

func depth(rel string) int { return strings.Count(rel, "/") }

// ---------------------------------------------------------------- ranking

// veto markers are never the public entry point of a library. A candidate carrying
// one is disqualified outright rather than merely demoted, so that the tool answers
// ASK instead of confidently recommending absl::abseil_test_dll. Exception: if the
// marker also occurs in the package's own name (googletest, gtest), it carries no
// signal and is ignored.
var veto = []string{
	"test", "benchmark", "bench", "example", "sample", "mock", "fuzz", "dll",
	"_dummy", "buildall",
}

// demote markers can legitimately be the answer (Catch2::Catch2WithMain) but are
// less likely than the plain form, so they are only penalised.
var demote = []string{
	"header-only", "header_only", "headeronly",
	"-static", "_static", "-shared", "_shared",
	"main", "tool", "util", "internal", "experimental",
}

// vetoed reports whether a candidate is disqualified from being the answer.
func vetoed(name string, aliases []string) (string, bool) {
	_, bare := split(strings.ToLower(name))
	for _, m := range veto {
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

func split(name string) (ns, bare string) {
	if i := strings.Index(name, "::"); i >= 0 {
		return name[:i], name[i+2:]
	}
	return "", name
}

// related reports whether a name plausibly belongs to this package at all.
// Used as a confidence gate: an unrelated top pick is worse than no answer.
func related(name string, aliases []string) bool {
	ns, bare := split(strings.ToLower(name))
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
			if n := commonPrefix(part, a); n >= 4 {
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

// rank scores candidates by how likely each is to be THE target a user should link.
func rank(cands []candidate, aliases []string) []candidate {
	for i := range cands {
		c := &cands[i]
		ns, bare := split(c.Name)
		lbare, lns := strings.ToLower(bare), strings.ToLower(ns)

		if ns != "" {
			c.Score += 50
			c.Why = append(c.Why, "+50 ns")
		}
		if c.IsAlias {
			c.Score += 20
			c.Why = append(c.Why, "+20 alias")
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
			c.Why = append(c.Why, fmt.Sprintf("+%d bare~pkg", best))
		}
		for _, a := range aliases {
			la := strings.ToLower(a)
			if lns != "" && la != "" && (lns == la || strings.HasPrefix(lns, la) || strings.HasPrefix(la, lns)) {
				c.Score += 25
				c.Why = append(c.Why, "+25 ns~pkg")
				break
			}
		}
		if lns != "" && lns == lbare {
			c.Score += 15
			c.Why = append(c.Why, "+15 ns==bare")
		}
		for _, d := range demote {
			if strings.Contains(lbare, d) {
				c.Score -= 30
				c.Why = append(c.Why, "-30 "+d)
				break
			}
		}
		if m, bad := vetoed(c.Name, aliases); bad {
			c.Score -= 1000
			c.Why = append(c.Why, "VETO:"+m)
		}
		if c.Kind == "INTERFACE" {
			c.Score += 5
			c.Why = append(c.Why, "+5 iface")
		}
		c.Score -= len(bare) / 8
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		return cands[i].Name < cands[j].Name
	})
	return cands
}

// ---------------------------------------------------------------- git

func clone(lib library, dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return "cached", nil
	}
	_ = os.RemoveAll(dir)
	run := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(lastLine(string(out))))
		}
		return nil
	}
	if err := run("clone", "--quiet", "--depth", "1", "--branch", lib.Tag,
		"--single-branch", "--", lib.URL, dir); err == nil {
		return "tag " + lib.Tag, nil
	}
	if err := run("clone", "--quiet", "--depth", "1", "--", lib.URL, dir); err != nil {
		return "", err
	}
	return "DEFAULT BRANCH (tag missing)", nil
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	return ls[len(ls)-1]
}

// ---------------------------------------------------------------- main

type result struct {
	lib        library
	ref        string
	project    string
	err        error
	cands      []candidate
	top1OK     bool
	top3OK     bool
	confident  bool
	totalCands int
}

func main() {
	keep := flag.Bool("keep", false, "reuse existing clones")
	work := flag.String("work", "tools/phase0/_work", "clone directory")
	flag.Parse()

	if err := os.MkdirAll(*work, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}
	if !*keep {
		fmt.Println("(use -keep to reuse clones)")
	}

	results := make([]result, 0, len(corpus))
	for _, lib := range corpus {
		start := time.Now()
		fmt.Printf("  %-14s ", lib.Name)
		dir := filepath.Join(*work, lib.Name)
		r := result{lib: lib}

		ref, err := clone(lib, dir)
		if err != nil {
			r.err = err
			fmt.Printf("CLONE FAILED: %v\n", err)
			results = append(results, r)
			continue
		}
		r.ref = ref

		cands, proj, err := scan(dir)
		if err != nil {
			r.err = err
			fmt.Printf("scan failed: %v\n", err)
			results = append(results, r)
			continue
		}
		r.project, r.totalCands = proj, len(cands)

		aliases := []string{lib.Name, proj}
		r.cands = rank(cands, aliases)

		exp := map[string]bool{}
		for _, e := range lib.Expected {
			exp[strings.ToLower(e)] = true
		}
		for i, c := range r.cands {
			if exp[strings.ToLower(c.Name)] {
				if i == 0 {
					r.top1OK = true
				}
				if i < 3 {
					r.top3OK = true
				}
			}
		}
		// Confidence gate: answer only if the top pick plausibly belongs to this
		// package and is not disqualified. Declining to guess (ASK) is a correct
		// outcome; a confident wrong answer is not.
		if len(r.cands) > 0 {
			_, bad := vetoed(r.cands[0].Name, aliases)
			r.confident = related(r.cands[0].Name, aliases) && !bad
		}

		fmt.Printf("%-28s %-5s %3d cands  project=%-22s %.1fs\n",
			ref, verdictOf(r), len(cands), proj, time.Since(start).Seconds())
		results = append(results, r)
	}

	report(results)
}

func verdictOf(r result) string {
	switch {
	case r.err != nil:
		return "ERR"
	case !r.confident:
		return "ASK"
	case r.top1OK:
		return "HIT"
	case r.top3OK:
		return "TOP3"
	default:
		return "WRONG"
	}
}

func report(results []result) {
	line := strings.Repeat("=", 86)
	fmt.Println("\n" + line)
	fmt.Println("PHASE 0 — static target inference, no recipes")
	fmt.Println(line)
	fmt.Printf("\n%-14s %-6s %-34s %s\n", "LIBRARY", "RESULT", "TOP PICK", "EXPECTED")
	fmt.Println(strings.Repeat("-", 86))

	var hit, top3, wrong, ask, errs int
	for _, r := range results {
		v := verdictOf(r)
		top := "(no confident answer)"
		if r.err != nil {
			top = "-"
		} else if len(r.cands) > 0 && r.confident {
			top = r.cands[0].Name
		}
		switch v {
		case "HIT":
			hit++
		case "TOP3":
			top3++
		case "WRONG":
			wrong++
		case "ASK":
			ask++
		case "ERR":
			errs++
		}
		fmt.Printf("%-14s %-6s %-34s %s\n", r.lib.Name, v, top, strings.Join(r.lib.Expected, " | "))
	}

	n := len(results)
	reachable := n - errs
	fmt.Println(strings.Repeat("-", 86))
	fmt.Printf("HIT   (top-1 correct)      : %d/%d   %.0f%% of reachable\n", hit, reachable, pct(hit, reachable))
	fmt.Printf("TOP3  (correct, not first) : %d\n", top3)
	fmt.Printf("ASK   (declined to guess)  : %d\n", ask)
	fmt.Printf("WRONG (confident + wrong)  : %d   <-- the number that must stay 0\n", wrong)
	fmt.Printf("ERR   (unreachable)        : %d\n", errs)

	fmt.Println("\n" + line)
	fmt.Println("DETAIL (top 4 ranked candidates, * = expected)")
	fmt.Println(line)
	for _, r := range results {
		if r.err != nil {
			continue
		}
		note := ""
		if r.lib.Note != "" {
			note = "  // " + r.lib.Note
		}
		fmt.Printf("\n%s  [%d candidates]%s\n", r.lib.Name, r.totalCands, note)
		if len(r.cands) == 0 {
			fmt.Println("    (none extractable — targets are built by helper macros)")
			continue
		}
		for i, c := range r.cands {
			if i >= 4 {
				break
			}
			mark := " "
			for _, e := range r.lib.Expected {
				if strings.EqualFold(e, c.Name) {
					mark = "*"
				}
			}
			fmt.Printf("  %s %d. %-36s score=%-5d %s\n", mark, i+1, c.Name, c.Score, strings.Join(c.Why, " "))
		}
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}
