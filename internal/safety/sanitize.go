package safety

import "strings"

// Sanitizer names, as they are spelled in `cppm build --sanitize=<name>` and as
// the generated preset names.
//
// A closed set of four, which is how the "ASan and TSan are mutually exclusive"
// rule is enforced: there is no name that means both, so the combination cannot
// be configured, only asked for and refused. They intercept allocation and
// relocate the heap, and a binary carrying both runtimes does not start.
const (
	SanAddress   = "asan"
	SanUndefined = "ubsan"
	SanBoth      = "asan+ubsan"
	SanThread    = "tsan"
)

// Sanitizers are the accepted names, in the order they are offered to the user:
// the common pairing first, because it is what most people want.
var Sanitizers = []string{SanBoth, SanAddress, SanUndefined, SanThread}

// Sanitizer describes one sanitizer build.
type Sanitizer struct {
	// Name is the preset name and the --sanitize value.
	Name string
	// Summary is one line for `cmake --list-presets` and for cppm's help.
	Summary string
	// GNUFlags are the compile flags for GCC and Clang. They are also the link
	// flags: a sanitizer compiled in but not linked produces a wall of
	// undefined references to __asan_* symbols, which is the single most
	// common way people get this wrong.
	GNUFlags []string
	// MSVCFlags are the MSVC equivalents, or nil when MSVC cannot do it.
	MSVCFlags []string
	// Env are the runtime options, applied to the test preset's environment.
	// A sanitizer that reports and carries on lets CI go green on a real
	// error, so every one of these halts.
	Env map[string]string
}

// sanitizers is the table. -O1 rather than -O0 is Google's own recommendation
// for ASan: it keeps the stack traces readable while being several times faster
// than an unoptimised build, which matters when the whole test suite runs under
// it.
var sanitizers = map[string]Sanitizer{
	SanAddress: {
		Name:    SanAddress,
		Summary: "AddressSanitizer: heap and stack overflow, use-after-free, leaks",
		// -fno-omit-frame-pointer is what makes the stack traces usable; without
		// it ASan reports the right error at the wrong place.
		GNUFlags:  []string{"-fsanitize=address", "-fno-omit-frame-pointer", "-g", "-O1"},
		MSVCFlags: []string{"/fsanitize=address", "/Zi"},
		Env: map[string]string{
			"ASAN_OPTIONS": "abort_on_error=1:detect_stack_use_after_return=1",
		},
	},
	SanUndefined: {
		Name:    SanUndefined,
		Summary: "UndefinedBehaviorSanitizer: signed overflow, bad shifts and casts, misaligned access",
		// -fno-sanitize-recover=all is the difference between a build that
		// reports undefined behaviour and one that fails on it. By default
		// UBSan prints and continues, so a test suite passes with the bug in it.
		GNUFlags: []string{"-fsanitize=undefined", "-fno-sanitize-recover=all", "-g", "-O1"},
		Env: map[string]string{
			"UBSAN_OPTIONS": "print_stacktrace=1:halt_on_error=1",
		},
	},
	SanBoth: {
		Name:      SanBoth,
		Summary:   "AddressSanitizer and UndefinedBehaviorSanitizer together: the usual pairing",
		GNUFlags:  []string{"-fsanitize=address,undefined", "-fno-sanitize-recover=all", "-fno-omit-frame-pointer", "-g", "-O1"},
		MSVCFlags: nil,
		Env: map[string]string{
			"ASAN_OPTIONS":  "abort_on_error=1:detect_stack_use_after_return=1",
			"UBSAN_OPTIONS": "print_stacktrace=1:halt_on_error=1",
		},
	},
	SanThread: {
		Name:     SanThread,
		Summary:  "ThreadSanitizer: data races and lock-order inversions",
		GNUFlags: []string{"-fsanitize=thread", "-g", "-O1"},
		Env: map[string]string{
			// A deadlock report naming only one of the two lock orders is
			// close to unactionable; this asks for both stacks.
			"TSAN_OPTIONS": "second_deadlock_stack=1:halt_on_error=1",
		},
	},
}

// FindSanitizer returns the named sanitizer.
func FindSanitizer(name string) (Sanitizer, bool) {
	s, ok := sanitizers[name]
	return s, ok
}

// ValidSanitizer reports whether name is a sanitizer cppm knows.
func ValidSanitizer(name string) bool {
	_, ok := sanitizers[name]
	return ok
}

// SanitizerSuggestion maps what somebody is likely to type to the name that
// exists, and explains the one combination that can never work.
//
// Returned separately from a plain "unknown value" error because the two wrong
// guesses here are predictable: a comma instead of a plus, and asking for ASan
// and TSan at once.
func SanitizerSuggestion(name string) string {
	norm := strings.ToLower(strings.TrimSpace(name))
	norm = strings.NewReplacer(",", "+", " ", "", "_", "+").Replace(norm)
	parts := strings.Split(norm, "+")

	seen := map[string]bool{}
	for _, p := range parts {
		switch p {
		case "address", "asan":
			seen["asan"] = true
		case "undefined", "ub", "ubsan":
			seen["ubsan"] = true
		case "thread", "tsan", "race", "helgrind":
			seen["tsan"] = true
		case "memory", "msan":
			seen["msan"] = true
		case "leak", "lsan":
			seen["lsan"] = true
		}
	}
	switch {
	case seen["asan"] && seen["tsan"]:
		return "ASan and TSan cannot be combined: both intercept allocation and relocate the heap, " +
			"so a binary carrying both runtimes does not start. Run them as two passes -- " +
			"--sanitize=asan then --sanitize=tsan"
	case seen["msan"]:
		return "MemorySanitizer is deliberately not offered: it needs every dependency instrumented, " +
			"including libc++, which means building a custom standard library. " +
			"'cppm test --memcheck' finds uninitialised reads without that cost"
	case seen["lsan"]:
		return "LeakSanitizer is part of --sanitize=asan on Linux; there is no separate name for it"
	case seen["asan"] && seen["ubsan"]:
		return "did you mean --sanitize=" + SanBoth + "?"
	case seen["asan"]:
		return "did you mean --sanitize=" + SanAddress + "?"
	case seen["ubsan"]:
		return "did you mean --sanitize=" + SanUndefined + "?"
	case seen["tsan"]:
		return "did you mean --sanitize=" + SanThread + "?"
	}
	return ""
}

// SanitizerCacheVar is the cache variable a sanitizer preset sets, and the one
// the generated include reads to decide which flags to emit.
//
// The flags are chosen in CMake rather than written into the preset, which is a
// deliberate departure from the roadmap's sketch. Only CMake knows which
// compiler is in use: MSVC can do /fsanitize=address and nothing else, and a
// preset carrying -fsanitize=thread would reach it as an unrecognised option
// rather than as the clear refusal this needs to be. It also keeps every
// compiler-family flag table in one package, as the warning levels already are.
//
// Private, per the _cppm_ convention: the user's CMakeLists.txt never
// references it, so it is not part of the interface cppm cannot migrate.
const SanitizerCacheVar = "_cppm_sanitize"
