package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/project"
	"github.com/mehdi-shokohi/cppm/internal/safety"
	"github.com/mehdi-shokohi/cppm/internal/toolchain"
	"github.com/mehdi-shokohi/cppm/internal/vcs"
)

// sanitizeFlagHelp is shared by build and test so the two cannot describe the
// same flag differently.
var sanitizeFlagHelp = "build under a sanitizer, in its own build tree: " +
	strings.Join(safety.Sanitizers, ", ")

// checkSanitizeFlag validates --sanitize before anything is configured.
//
// The two wrong guesses here are predictable -- a comma instead of a plus, and
// asking for ASan and TSan at once -- so each gets its own answer rather than a
// list of valid values. "ASan and TSan are mutually exclusive" has to arrive
// here, as a sentence, and not later as a link failure.
func checkSanitizeFlag(name string) error {
	if name == "" || safety.ValidSanitizer(name) {
		return nil
	}
	msg := fmt.Sprintf("--sanitize %q is not a sanitizer cppm knows; expected one of %s",
		name, strings.Join(safety.Sanitizers, ", "))
	if hint := safety.SanitizerSuggestion(name); hint != "" {
		msg += "\n  " + hint
	}
	return fmt.Errorf("%s", msg)
}

// presetFor returns the preset a build or test run should use.
//
// A sanitizer gets its own preset and therefore its own build tree. Sharing
// one with debug would mix instrumented and uninstrumented objects, which
// links successfully and then crashes inside the sanitizer's own runtime --
// a long way from the bug the user was looking for.
func presetFor(sanitize string, release bool) string {
	if sanitize != "" {
		return sanitize
	}
	if release {
		return "release"
	}
	return "debug"
}

// sanitizerOfBuildTree reports which sanitizer an existing build tree was
// configured with, by reading the cache variable cppm's presets set.
//
// Read from the tree rather than inferred from the flags, because the question
// being asked is what the binaries on disk actually are -- which is what
// decides whether valgrind may be pointed at them.
func sanitizerOfBuildTree(p *project.Project, preset string) string {
	b, err := os.ReadFile(filepath.Join(p.BuildDir(preset), "CMakeCache.txt"))
	if err != nil {
		return ""
	}
	return cacheValue(string(b), safety.SanitizerCacheVar)
}

// checkInjectedSanitizers refuses a sanitizer build that a dependency will
// overrule, and names the option that turns the dependency's own sanitizer off.
//
// Reaching a dependency is not the same as having the last word on it. cppm's
// flags are set before the dependencies are made available, so they reach
// them -- but a dependency may append its own afterwards, and duckdb does:
// ENABLE_SANITIZER and ENABLE_UBSAN both default ON and append
// -fsanitize=address to CMAKE_CXX_FLAGS_DEBUG. Without this check,
// `--sanitize=tsan` against a default duckdb passes cppm's mutual-exclusion
// test and then fails in the compiler, with an error naming neither cppm nor
// duckdb.
func checkInjectedSanitizers(p *project.Project, want safety.Sanitizer, preset string) error {
	var conflicts []safety.Injection
	for _, name := range p.Manifest.DependencyNames() {
		for _, root := range dependencySourceRoots(p, name, preset) {
			for _, inj := range safety.ScanInjectedSanitizers(name, root) {
				// An option the user has already set OFF in cppm.json is not a
				// problem: cppm emits it as a cache variable before the
				// dependency is configured, so the branch never runs.
				if inj.Option != "" && isOptionOff(p, name, inj.Option) {
					continue
				}
				if inj.Conflicts(want) {
					conflicts = append(conflicts, inj)
				}
			}
			if len(conflicts) > 0 {
				break // one root per dependency is enough to find this
			}
		}
	}
	if len(conflicts) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--sanitize=%s cannot work in this project: a dependency adds a conflicting sanitizer of its own.\n",
		want.Name)
	for _, c := range conflicts {
		fmt.Fprintf(&b, "\n  %s adds -fsanitize=%s at %s:%d\n",
			c.Dependency, strings.Join(c.Sanitizers, ","), c.File, c.Line)
		if c.Option == "" {
			fmt.Fprintf(&b, "    no option guards it, so it cannot be turned off; run --sanitize=%s instead,\n",
				suggestCompatible(c))
			fmt.Fprintf(&b, "    or 'cppm test --helgrind', which needs no recompilation\n")
			continue
		}
		on := ""
		if c.OptionDefaultsOn {
			on = " (it defaults ON, which is why this happens without being asked for)"
		}
		fmt.Fprintf(&b, "    it is guarded by %s%s. Turn it off in cppm.json:\n", c.Option, on)
		fmt.Fprintf(&b, "        \"dependencies\": { %q: { ..., \"options\": { %q: \"OFF\" } } }\n",
			c.Dependency, c.Option)
		b.WriteString("    then run 'cppm sync'\n")
	}
	b.WriteString("\nASan and TSan both intercept allocation and relocate the heap, so a binary\n")
	b.WriteString("carrying both runtimes does not start. The compiler would reject this, with\n")
	b.WriteString("an error naming neither cppm nor the dependency.")
	return fmt.Errorf("%s", b.String())
}

// suggestCompatible names a sanitizer that does not clash with what a
// dependency already injects, so the error ends with something to run.
func suggestCompatible(c safety.Injection) string {
	for _, got := range c.Sanitizers {
		if got == "thread" {
			return safety.SanThread
		}
	}
	return safety.SanBoth
}

// isOptionOff reports whether cppm.json already sets a dependency option to a
// false value.
func isOptionOff(p *project.Project, dep, option string) bool {
	d, ok := p.Manifest.Dependencies[dep]
	if !ok {
		return false
	}
	for k, v := range d.Options {
		if !strings.EqualFold(k, option) {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(v)) {
		case "OFF", "FALSE", "0", "NO", "N", "IGNORE", "NOTFOUND":
			return true
		}
	}
	return false
}

// dependencySourceRoots lists the places a dependency's sources may be found,
// most authoritative first.
//
// The _deps tree in a configured build directory is what CMake actually read,
// so it wins over the cache. Both are optional: before the first configure
// neither may exist, and the scan then reports nothing, which is "cannot tell"
// rather than "clean".
func dependencySourceRoots(p *project.Project, name, preset string) []string {
	var roots []string

	deps := filepath.Join(p.BuildDir(preset), "_deps")
	if entries, err := os.ReadDir(deps); err == nil {
		var matches []string
		lower := strings.ToLower(name)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// FetchContent names these <lowercase-dep>-src, -build and -subbuild.
			if n := strings.ToLower(e.Name()); n == lower+"-src" {
				matches = append(matches, filepath.Join(deps, e.Name()))
			}
		}
		sort.Strings(matches)
		roots = append(roots, matches...)
	}

	if pkg, ok := p.Lock.Find(name); ok {
		switch {
		case pkg.Path != "":
			roots = append(roots, filepath.Join(p.Root, pkg.Path))
		case pkg.URL != "" && pkg.Commit != "":
			roots = append(roots, vcs.SourceDir(pkg.URL, pkg.Commit))
		}
	}
	return roots
}

// tsanNeedsFixedAddressSpace reports whether a TSan run has to be launched
// with address-space randomisation turned off.
//
// ThreadSanitizer maps its shadow memory at fixed addresses, and a kernel
// configured with high-entropy ASLR -- which is the default on current Ubuntu,
// Fedora and Arch -- can place the binary where that mapping will not fit.
// TSan then dies before main() with
//
//	FATAL: ThreadSanitizer: unexpected memory mapping 0x...
//
// which names neither the cause nor the fix, and reads like a broken toolchain
// rather than a kernel setting. A --sanitize=tsan preset that fails this way on
// a stock distribution is not a feature, so cppm launches the run with ASLR
// disabled instead of letting the user find this out.
//
// Linux only: it is a Linux kernel setting, and setarch is Linux-only too.
func tsanNeedsFixedAddressSpace(sanitize string) bool {
	return sanitize == safety.SanThread && !isWindows() && !isDarwin()
}

// aslrDisablingPrefix returns the argv prefix that runs a command with
// address-space randomisation off, or nil when nothing is available.
//
// setarch(1) ships with util-linux, so it is present on essentially every
// Linux system. Disabling ASLR for the duration of a race-detector run changes
// nothing a race detector measures; it is standard practice for TSan and for
// reproducible debugging generally.
func aslrDisablingPrefix() []string {
	if sa := toolchain.Setarch(); sa.Found() {
		return []string{sa.Path, "-R"}
	}
	return nil
}

// tsanASLRHint is what to say when the launcher is unavailable, so a failure
// arrives with its remedy attached rather than as an unexplained crash.
const tsanASLRHint = "setarch is not installed, so cppm cannot disable address-space randomisation for this run.\n" +
	"if TSan dies with \"unexpected memory mapping\", that is why. Either install util-linux, or:\n" +
	"    sudo sysctl vm.mmap_rnd_bits=28"
