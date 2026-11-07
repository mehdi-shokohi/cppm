package safety

import (
	"strings"
	"testing"
)

// ASan and TSan are mutually exclusive, and this is how that is enforced:
// there is no name meaning both, so the combination cannot be configured at
// all. Mutual exclusion by construction rather than by a check somebody can
// forget to run.
func TestNoSanitizerCombinesAddressAndThread(t *testing.T) {
	for _, name := range Sanitizers {
		s, ok := FindSanitizer(name)
		if !ok {
			t.Fatalf("%q is offered but not defined", name)
		}
		flags := strings.Join(s.GNUFlags, " ")
		if strings.Contains(flags, "address") && strings.Contains(flags, "thread") {
			t.Errorf("%q combines ASan and TSan; both intercept allocation and relocate the heap, "+
				"so the binary would not start", name)
		}
	}
}

// A sanitizer that reports and carries on lets a test suite pass with the bug
// still in it, which is worse than not running it. Every sanitizer here has to
// stop.
func TestEverySanitizerHalts(t *testing.T) {
	for _, name := range Sanitizers {
		s, _ := FindSanitizer(name)
		flags := strings.Join(s.GNUFlags, " ")
		env := strings.Join(sortedValues(s.Env), " ")

		switch {
		case strings.Contains(flags, "undefined"):
			// UBSan's default is to print and continue, so without this the
			// suite goes green with undefined behaviour in it.
			if !strings.Contains(flags, "-fno-sanitize-recover") {
				t.Errorf("%q enables UBSan without -fno-sanitize-recover, so it reports and carries on", name)
			}
		case strings.Contains(flags, "address"):
			if !strings.Contains(env, "abort_on_error=1") {
				t.Errorf("%q enables ASan without abort_on_error=1", name)
			}
		case strings.Contains(flags, "thread"):
			if !strings.Contains(env, "halt_on_error=1") {
				t.Errorf("%q enables TSan without halt_on_error=1", name)
			}
		}
		if len(s.Env) == 0 {
			t.Errorf("%q sets no runtime options at all", name)
		}
	}
}

// ASan's stack traces are only usable with frame pointers. Without this the
// error is reported at the wrong place, which costs more time than the
// sanitizer saves.
func TestAddressSanitizerKeepsFramePointers(t *testing.T) {
	for _, name := range Sanitizers {
		s, _ := FindSanitizer(name)
		flags := strings.Join(s.GNUFlags, " ")
		if !strings.Contains(flags, "sanitize=address") && !strings.Contains(flags, "address,") {
			continue
		}
		if !strings.Contains(flags, "-fno-omit-frame-pointer") {
			t.Errorf("%q enables ASan without -fno-omit-frame-pointer, so its stack traces point at the wrong frame", name)
		}
	}
}

// Every sanitizer carries -g, or the report names addresses instead of lines.
// And -O1 rather than -O0: it is Google's own recommendation for ASan, several
// times faster while keeping the traces readable.
func TestSanitizersAreDebuggableAndLightlyOptimised(t *testing.T) {
	for _, name := range Sanitizers {
		s, _ := FindSanitizer(name)
		flags := strings.Join(s.GNUFlags, " ")
		if !strings.Contains(flags, "-g") {
			t.Errorf("%q has no -g, so its reports name addresses rather than lines", name)
		}
		if !strings.Contains(flags, "-O1") {
			t.Errorf("%q does not use -O1", name)
		}
	}
}

// MSVC implements AddressSanitizer and nothing else. The table has to say which
// is which, so the generated CMake can refuse the others with that explanation
// instead of letting the compiler report an unrecognised option.
func TestOnlyAddressSanitizerIsAvailableOnMSVC(t *testing.T) {
	asan, _ := FindSanitizer(SanAddress)
	if len(asan.MSVCFlags) == 0 {
		t.Error("MSVC does support AddressSanitizer, so asan must have MSVC flags")
	}
	for _, name := range []string{SanUndefined, SanThread, SanBoth} {
		s, _ := FindSanitizer(name)
		if len(s.MSVCFlags) != 0 {
			t.Errorf("%q claims MSVC support; MSVC has no UBSan and no TSan", name)
		}
	}
}

// The two wrong guesses are predictable, so each gets an answer rather than a
// list of valid values.
func TestSanitizerSuggestionAnswersThePredictableMistakes(t *testing.T) {
	cases := []struct{ input, want string }{
		{"asan+tsan", "cannot be combined"},
		{"tsan+asan", "cannot be combined"},
		{"asan,ubsan", "asan+ubsan"},
		{"address", "asan"},
		{"thread", "tsan"},
		{"msan", "MemorySanitizer"},
		{"memory", "MemorySanitizer"},
		{"lsan", "LeakSanitizer"},
	}
	for _, c := range cases {
		got := SanitizerSuggestion(c.input)
		if !strings.Contains(got, c.want) {
			t.Errorf("SanitizerSuggestion(%q) = %q, expected it to mention %q", c.input, got, c.want)
		}
	}
	// Nothing invented for something genuinely unrecognisable.
	if got := SanitizerSuggestion("zzzz"); got != "" {
		t.Errorf("SanitizerSuggestion(%q) = %q, want no suggestion", "zzzz", got)
	}
}

// --error-exitcode=1 is what makes a valgrind run able to fail CI at all.
// Without it valgrind reports the error, exits 0, and CTest records a pass.
func TestEveryValgrindErrorToolFailsOnError(t *testing.T) {
	for _, name := range ValgrindTools {
		tool, ok := FindValgrindTool(name)
		if !ok {
			t.Fatalf("%q is offered but not defined", name)
		}
		opts := strings.Join(tool.Options, " ")
		if !strings.Contains(opts, "--tool="+name) {
			t.Errorf("%q does not select its own valgrind tool: %q", name, opts)
		}
		if tool.FindsErrors {
			if !strings.Contains(opts, "--error-exitcode=1") {
				t.Errorf("%q can report errors but has no --error-exitcode, so CI passes with the bug in it", name)
			}
		} else {
			// A profiler has no verdict, so cppm has to say where the output
			// went rather than report a clean run.
			if tool.Output == "" {
				t.Errorf("%q produces a profile but names no output file", name)
			}
			if strings.Contains(opts, "--error-exitcode") {
				t.Errorf("%q is a profiler; --error-exitcode means nothing for it", name)
			}
		}
	}
}

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
