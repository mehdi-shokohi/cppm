// Command phase0 measures how often cppm can infer the correct CMake link target
// for a real library using only a static source scan, with no curated recipe.
//
// Rationale: CMake cannot enumerate ALIAS targets, which are exactly the names users
// are supposed to link (fmt::fmt, spdlog::spdlog). They can however be recovered by
// scanning the source. This harness measures how reliable that recovery is.
//
// The scan/rank logic here is the prototype of internal/discover.
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
		[]string{"nlohmann_json::nlohmann_json"}, "target name unrelated to repo name"},
	{"Catch2", "https://github.com/catchorg/Catch2.git", "v3.7.1",
		[]string{"Catch2::Catch2WithMain", "Catch2::Catch2"}, "two legitimate answers"},
	{"googletest", "https://github.com/google/googletest.git", "v1.15.2",
		[]string{"GTest::gtest_main", "GTest::gtest"}, "multi-target"},
	{"eigen", "https://gitlab.com/libeigen/eigen.git", "3.4.0",
		[]string{"Eigen3::Eigen"}, "non-GitHub host, header-only"},
	{"abseil", "https://github.com/abseil/abseil-cpp.git", "20240722.0",
		[]string{"absl::base", "absl::strings"}, "hundreds of targets, no single entry point"},
	{"SDL2", "https://github.com/libsdl-org/SDL.git", "release-2.30.9",
		[]string{"SDL2::SDL2"}, "release-* tag form"},
	{"cxxopts", "https://github.com/jarro2783/cxxopts.git", "v3.2.0",
		[]string{"cxxopts::cxxopts"}, ""},
	{"tomlplusplus", "https://github.com/marzer/tomlplusplus.git", "v3.4.0",
		[]string{"tomlplusplus::tomlplusplus"}, ""},
}

// ---------------------------------------------------------------- source scan

var (
	reAlias = regexp.MustCompile(`(?is)\badd_library\s*\(\s*([A-Za-z0-9_:.$\{\}-]+)\s+ALIAS\s+([A-Za-z0-9_.$\{\}-]+)\s*\)`)
	reReal  = regexp.MustCompile(`(?is)\badd_library\s*\(\s*([A-Za-z0-9_:.$\{\}-]+)\s+(INTERFACE|STATIC|SHARED|MODULE|OBJECT)\b`)
	reProj  = regexp.MustCompile(`(?is)\bproject\s*\(\s*([A-Za-z0-9_.-]+)`)
)

// skipDir filters out directories whose targets are never the public API.
func skipDir(rel string) bool {
	p := strings.ToLower(filepath.ToSlash(rel))
	for _, bad := range []string{
		"test/", "tests/", "example/", "examples/", "benchmark/", "benchmarks/",
		"third_party/", "thirdparty/", "extern/", "external/", "vendor/",
		"doc/", "docs/", "fuzz/", "fuzzing/", "sample/", "samples/", "tools/",
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
	Kind    string // INTERFACE, STATIC, ... or "ALIAS"
	Score   int
	Why     []string
}

// scan walks a cloned source tree and extracts every declared library target name.
func scan(root string) ([]candidate, string, error) {
	var projectName string
	seen := map[string]*candidate{}

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
		text := string(b)

		// Capture the top-level project() name so ${PROJECT_NAME} can be resolved.
		if rel == "CMakeLists.txt" {
			if m := reProj.FindStringSubmatch(text); m != nil {
				projectName = m[1]
			}
		}

		add := func(name, kind string, isAlias bool) {
			name = strings.TrimSpace(name)
			if projectName != "" {
				name = strings.ReplaceAll(name, "${PROJECT_NAME}", projectName)
				name = strings.ReplaceAll(name, "${PROJECT_NAME_LOWER}", strings.ToLower(projectName))
			}
			// Unresolvable variable reference — cannot be used as a link name.
			if strings.Contains(name, "$") || name == "" {
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

		for _, m := range reAlias.FindAllStringSubmatch(text, -1) {
			add(m[1], "ALIAS", true)
		}
		for _, m := range reReal.FindAllStringSubmatch(text, -1) {
			add(m[1], strings.ToUpper(m[2]), false)
		}
		return nil
	})
	if err != nil {
		return nil, projectName, err
	}

	out := make([]candidate, 0, len(seen))
	for _, c := range seen {
		out = append(out, *c)
	}
	return out, projectName, nil
}

// ---------------------------------------------------------------- ranking

var demote = []string{
	"header-only", "header_only", "headeronly",
	"-static", "_static", "-shared", "_shared",
	"test", "benchmark", "example", "sample", "mock", "fuzz",
	"main", "tool", "util", "internal", "experimental",
}

// rank scores candidates by how likely each is to be THE target a user should link.
func rank(cands []candidate, pkg string) []candidate {
	lpkg := strings.ToLower(pkg)
	// Common repo-name -> target-name drift (SDL repo exports SDL2, etc.)
	for i := range cands {
		c := &cands[i]
		name := c.Name
		bare := name
		ns := ""
		if i := strings.Index(name, "::"); i >= 0 {
			ns, bare = name[:i], name[i+2:]
		}
		lbare, lns := strings.ToLower(bare), strings.ToLower(ns)

		if strings.Contains(name, "::") {
			c.Score += 50
			c.Why = append(c.Why, "+50 namespaced")
		}
		if c.IsAlias {
			c.Score += 20
			c.Why = append(c.Why, "+20 alias")
		}
		// Namespace or bare name echoing the package name is a strong signal.
		switch {
		case lbare == lpkg:
			c.Score += 40
			c.Why = append(c.Why, "+40 bare==pkg")
		case strings.HasPrefix(lbare, lpkg) || strings.HasPrefix(lpkg, lbare):
			c.Score += 20
			c.Why = append(c.Why, "+20 bare~pkg")
		}
		if lns != "" && (lns == lpkg || strings.HasPrefix(lns, lpkg) || strings.HasPrefix(lpkg, lns)) {
			c.Score += 25
			c.Why = append(c.Why, "+25 ns~pkg")
		}
		// ns::ns (fmt::fmt, absl::absl-style canonical form)
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
		if c.Kind == "INTERFACE" {
			c.Score += 5
			c.Why = append(c.Why, "+5 interface")
		}
		// Shorter names are usually the public entry point.
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
	// Try the requested tag shallow; fall back to the default branch.
	if err := run("clone", "--quiet", "--depth", "1", "--branch", lib.Tag,
		"--single-branch", "--", lib.URL, dir); err == nil {
		return "tag " + lib.Tag, nil
	}
	if err := run("clone", "--quiet", "--depth", "1", "--", lib.URL, dir); err != nil {
		return "", err
	}
	return "DEFAULT BRANCH (tag " + lib.Tag + " missing)", nil
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	return ls[len(ls)-1]
}

// ---------------------------------------------------------------- main

type result struct {
	lib        library
	ref        string
	err        error
	cands      []candidate
	top1OK     bool
	top3OK     bool
	totalCands int
}

func main() {
	keep := flag.Bool("keep", false, "reuse existing clones")
	work := flag.String("work", "tools/phase0/_work", "clone directory")
	flag.Parse()

	if !*keep {
		_ = os.RemoveAll(*work)
	}
	if err := os.MkdirAll(*work, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}

	results := make([]result, 0, len(corpus))
	for _, lib := range corpus {
		start := time.Now()
		fmt.Printf("  %-14s cloning... ", lib.Name)
		dir := filepath.Join(*work, lib.Name)
		r := result{lib: lib}
		ref, err := clone(lib, dir)
		if err != nil {
			r.err = err
			fmt.Printf("FAILED (%v)\n", err)
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
		r.totalCands = len(cands)
		r.cands = rank(cands, lib.Name)

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
		verdict := "MISS"
		if r.top1OK {
			verdict = "HIT "
		} else if r.top3OK {
			verdict = "top3"
		}
		fmt.Printf("%s  %-4s %3d candidates  (%s, project=%s, %.1fs)\n",
			ref, verdict, len(cands), lib.Name, proj, time.Since(start).Seconds())
		results = append(results, r)
	}

	// -------------------------------------------------- report
	fmt.Println("\n" + strings.Repeat("=", 78))
	fmt.Println("PHASE 0 RESULT — static target inference, no recipes")
	fmt.Println(strings.Repeat("=", 78))
	fmt.Printf("\n%-14s %-6s %-34s %s\n", "LIBRARY", "RESULT", "TOP PICK", "EXPECTED")
	fmt.Println(strings.Repeat("-", 78))

	var hit, top3, fail int
	for _, r := range results {
		if r.err != nil {
			fail++
			fmt.Printf("%-14s %-6s %-34s %s\n", r.lib.Name, "ERROR", "-", strings.Join(r.lib.Expected, " | "))
			continue
		}
		top := "(none found)"
		if len(r.cands) > 0 {
			top = r.cands[0].Name
		}
		verdict := "MISS"
		if r.top1OK {
			verdict = "HIT"
			hit++
		} else if r.top3OK {
			verdict = "TOP3"
			top3++
		}
		fmt.Printf("%-14s %-6s %-34s %s\n", r.lib.Name, verdict, top, strings.Join(r.lib.Expected, " | "))
	}

	n := len(results)
	fmt.Println(strings.Repeat("-", 78))
	fmt.Printf("top-1 correct : %d/%d  (%.0f%%)\n", hit, n, pct(hit, n))
	fmt.Printf("top-3 correct : %d/%d  (%.0f%%)\n", hit+top3, n, pct(hit+top3, n))
	fmt.Printf("errors        : %d\n", fail)

	fmt.Println("\n" + strings.Repeat("=", 78))
	fmt.Println("PER-LIBRARY DETAIL (top 4 ranked candidates)")
	fmt.Println(strings.Repeat("=", 78))
	for _, r := range results {
		if r.err != nil {
			continue
		}
		note := ""
		if r.lib.Note != "" {
			note = "  // " + r.lib.Note
		}
		fmt.Printf("\n%s  [%d candidates]%s\n", r.lib.Name, r.totalCands, note)
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
	fmt.Println("\n(* marks the expected answer)")
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}
