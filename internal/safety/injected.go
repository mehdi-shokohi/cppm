package safety

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Injection is a sanitizer flag a dependency adds to the build itself.
//
// This exists because reaching a dependency is not the same as having the last
// word on it. cppm's sanitizer flags are set before the dependencies are made
// available, so they reach them -- but a dependency is free to append flags of
// its own afterwards, and at least one popular one does: duckdb's
// ENABLE_SANITIZER and ENABLE_UBSAN both default ON and append
// -fsanitize=address -fsanitize=undefined to CMAKE_CXX_FLAGS_DEBUG. So
// `--sanitize=tsan` against a default duckdb puts ASan and TSan in one
// translation unit, and the compiler rejects it -- long after cppm's own
// mutual-exclusion check has passed, with an error naming neither cppm nor
// duckdb.
type Injection struct {
	// Dependency is the name the caller scanned for, echoed back.
	Dependency string
	// File is the CMake file, relative to the scanned root.
	File string
	// Line is 1-indexed.
	Line int
	// Sanitizers are the -fsanitize= values on that line, e.g.
	// ["address", "undefined"].
	Sanitizers []string
	// Option is the CMake option guarding it, when one could be identified.
	// This is the actionable half: it is what the user sets OFF.
	Option string
	// OptionDefaultsOn records that the guarding option is declared with an ON
	// or TRUE default, which is what makes the injection happen without
	// anybody asking for it.
	OptionDefaultsOn bool
}

// Conflicts reports whether this injection cannot coexist with a sanitizer.
//
// Only the ASan/TSan pair is a hard conflict: they both intercept allocation
// and relocate the heap. UBSan composes with either.
func (i Injection) Conflicts(want Sanitizer) bool {
	wanted := map[string]bool{}
	for _, f := range want.GNUFlags {
		for _, s := range parseSanitizeFlag(f) {
			wanted[s] = true
		}
	}
	for _, got := range i.Sanitizers {
		if got == "thread" && (wanted["address"] || wanted["leak"]) {
			return true
		}
		if (got == "address" || got == "leak") && wanted["thread"] {
			return true
		}
	}
	return false
}

var (
	sanitizeFlagRe = regexp.MustCompile(`[-/]fsanitize=([A-Za-z0-9_,.-]+)`)
	// An option() or a cached BOOL whose default turns the injection on
	// without anybody asking.
	optionOnRe  = regexp.MustCompile(`(?i)^\s*option\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)\s+("(?:[^"\\]|\\.)*"|[^\s)]+)\s+(ON|TRUE|1)\s*\)`)
	optionAnyRe = regexp.MustCompile(`(?i)^\s*option\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)`)
	identRe     = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
)

// cmakeConditionNoise are words that appear in an if() condition without being
// the option somebody would turn off.
var cmakeConditionNoise = map[string]bool{
	"not": true, "and": true, "or": true, "if": true, "elseif": true,
	"defined": true, "target": true, "exists": true, "strequal": true,
	"matches": true, "version_greater": true, "version_less": true,
	"version_equal": true, "version_greater_equal": true, "version_less_equal": true,
	"in_list": true, "command": true, "policy": true, "test": true,
	"equal": true, "less": true, "greater": true, "greater_equal": true,
	"less_equal": true, "cmake_cxx_compiler_id": true, "cmake_build_type": true,
	"on": true, "off": true, "true": true, "false": true, "msvc": true,
	"win32": true, "unix": true, "apple": true, "android": true,
}

// scanLimits keep this bounded. A dependency tree can be enormous -- duckdb is
// tens of thousands of files -- and this runs before every sanitizer build, so
// it has to cost nothing noticeable.
const (
	maxScanFiles    = 6000
	maxScanFileSize = 1 << 20
)

// ScanInjectedSanitizers walks a dependency's source tree for sanitizer flags
// the dependency adds itself, and names the option that turns each one off.
//
// root is a directory holding the dependency's sources: either cppm's source
// cache or the _deps tree in a configured build directory. A root that does not
// exist yields nothing rather than an error -- the caller is a diagnostic, and
// a missing tree means "cannot tell", not "clean".
func ScanInjectedSanitizers(dependency, root string) []Injection {
	var out []Injection
	files := 0

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner of a dependency is not fatal here
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".svn", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !isCMakeFile(d.Name()) {
			return nil
		}
		if files++; files > maxScanFiles {
			return fs.SkipAll
		}
		if info, err := d.Info(); err == nil && info.Size() > maxScanFileSize {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		out = append(out, scanCMakeText(dependency, filepath.ToSlash(rel), string(b))...)
		return nil
	})

	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func isCMakeFile(name string) bool {
	return name == "CMakeLists.txt" || strings.HasSuffix(name, ".cmake")
}

// scanCMakeText finds sanitizer flags in one CMake file and attributes each to
// the option that guards it.
func scanCMakeText(dependency, file, text string) []Injection {
	lines := strings.Split(text, "\n")

	// Options declared ON in this file. The default matters: an option that is
	// OFF until asked for is not an injection, it is a feature.
	defaultsOn := map[string]bool{}
	declared := map[string]bool{}
	for _, l := range lines {
		if m := optionOnRe.FindStringSubmatch(l); m != nil {
			defaultsOn[strings.ToUpper(m[1])] = true
		}
		if m := optionAnyRe.FindStringSubmatch(l); m != nil {
			declared[strings.ToUpper(m[1])] = true
		}
	}

	var out []Injection
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		m := sanitizeFlagRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		inj := Injection{
			Dependency: dependency,
			File:       file,
			Line:       i + 1,
			Sanitizers: parseSanitizeFlag(m[0]),
		}
		if opt, on := guardingOption(lines, i, defaultsOn, declared); opt != "" {
			inj.Option, inj.OptionDefaultsOn = opt, on
		}
		out = append(out, inj)
	}
	return out
}

// parseSanitizeFlag splits "-fsanitize=address,undefined" into its values.
func parseSanitizeFlag(flag string) []string {
	m := sanitizeFlagRe.FindStringSubmatch(flag)
	if m == nil {
		return nil
	}
	var out []string
	for _, p := range strings.Split(m[1], ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.ToLower(p))
		}
	}
	return out
}

// guardingOption walks backwards from a line to the innermost if() that
// encloses it and picks the identifier a user would set to turn it off.
//
// Backwards and nesting-aware rather than a plain search, because the nearest
// preceding if() is frequently a closed block that has nothing to do with the
// line in question.
func guardingOption(lines []string, from int, defaultsOn, declared map[string]bool) (string, bool) {
	depth := 0
	var fallback string
	for i := from - 1; i >= 0; i-- {
		l := strings.ToLower(strings.TrimSpace(lines[i]))
		if strings.HasPrefix(l, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(l, "endif"):
			depth++
		case strings.HasPrefix(l, "endfunction"), strings.HasPrefix(l, "endmacro"):
			return fallback, fallback != "" && defaultsOn[fallback]
		case strings.HasPrefix(l, "if(") || strings.HasPrefix(l, "if ("):
			if depth > 0 {
				depth--
				continue
			}
			for _, ident := range conditionIdentifiers(lines[i]) {
				if defaultsOn[ident] {
					return ident, true
				}
				if declared[ident] && fallback == "" {
					fallback = ident
				}
				if fallback == "" {
					fallback = ident
				}
			}
		}
	}
	return fallback, fallback != "" && defaultsOn[fallback]
}

// conditionIdentifiers pulls the plausible option names out of an if() line.
func conditionIdentifiers(line string) []string {
	open := strings.Index(line, "(")
	if open < 0 {
		return nil
	}
	cond := line[open+1:]
	if close := strings.LastIndex(cond, ")"); close >= 0 {
		cond = cond[:close]
	}
	var out []string
	for _, ident := range identRe.FindAllString(cond, -1) {
		if cmakeConditionNoise[strings.ToLower(ident)] {
			continue
		}
		// An option name is conventionally shouty. A lowercase local variable
		// is not something the user can set from cppm.json.
		if ident != strings.ToUpper(ident) {
			continue
		}
		out = append(out, strings.ToUpper(ident))
	}
	return out
}
