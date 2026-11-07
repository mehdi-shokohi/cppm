package safety

import (
	"strings"
	"testing"
)

// The levels are ordered by strictness, and each one must be a superset of the
// one below it. A "strict" that dropped a flag "default" had would mean turning
// the level up could turn a warning off, which nobody would predict.
func TestLevelsAreCumulative(t *testing.T) {
	for _, fn := range []struct {
		name  string
		flags func(string) []string
	}{
		{"GNU", GNUWarnings},
		{"MSVC", MSVCWarnings},
	} {
		// MSVC does not accumulate textually -- /W4 replaces /W3 rather than
		// adding to it -- so the check is on intent, not on set inclusion.
		if fn.name == "MSVC" {
			continue
		}
		prev := fn.flags(WarningsDefault)
		for _, level := range []string{WarningsStrict, WarningsError} {
			got := fn.flags(level)
			for _, f := range prev {
				if !contains(got, f) {
					t.Errorf("%s %q drops %q, which %q had: turning the level up must never turn a warning off",
						fn.name, level, f, WarningsDefault)
				}
			}
			prev = got
		}
	}
}

// "off" means no flags at all, on every compiler. It is the escape hatch for
// adopting cppm in a codebase that cannot yet survive -Wall, so it has to be
// genuinely empty rather than quieter.
func TestOffEmitsNothing(t *testing.T) {
	if got := GNUWarnings(WarningsOff); len(got) != 0 {
		t.Errorf("GNUWarnings(off) = %v, want none", got)
	}
	if got := MSVCWarnings(WarningsOff); len(got) != 0 {
		t.Errorf("MSVCWarnings(off) = %v, want none", got)
	}
}

// Only "error" may carry a flag that fails a build. A -Werror that arrived one
// level early would turn a routine cppm upgrade into a project that no longer
// compiles.
func TestOnlyErrorLevelFailsTheBuild(t *testing.T) {
	for _, level := range []string{WarningsOff, WarningsDefault, WarningsStrict} {
		if contains(GNUWarnings(level), "-Werror") {
			t.Errorf("GNU level %q contains -Werror; only %q may", level, WarningsError)
		}
		if contains(MSVCWarnings(level), "/WX") {
			t.Errorf("MSVC level %q contains /WX; only %q may", level, WarningsError)
		}
	}
	if !contains(GNUWarnings(WarningsError), "-Werror") {
		t.Error("the error level must contain -Werror")
	}
	if !contains(MSVCWarnings(WarningsError), "/WX") {
		t.Error("the error level must contain /WX on MSVC")
	}
}

// An unknown level must produce no flags rather than a partial set. Validation
// rejects a typo long before this, but if that check were ever bypassed, a
// silently empty result is a project with no warnings -- visible -- rather than
// one with the wrong ones.
func TestUnknownLevelIsEmpty(t *testing.T) {
	if got := GNUWarnings("pedantic"); len(got) != 0 {
		t.Errorf("GNUWarnings(unknown) = %v, want none", got)
	}
	if !ValidWarnings(DefaultWarnings) {
		t.Error("the default level must itself be a valid level")
	}
	if ValidWarnings("pedantic") {
		t.Error("ValidWarnings accepted a level that does not exist")
	}
}

// The -U must come before the -D. Ubuntu, Fedora and SUSE define
// _FORTIFY_SOURCE in their GCC specs, so a bare -D warns "macro redefined" on
// every translation unit -- the undefine is what makes it quiet.
func TestFortifyUndefinesBeforeDefining(t *testing.T) {
	u, d := -1, -1
	for i, f := range GNUFortify {
		if strings.HasPrefix(f, "-U_FORTIFY_SOURCE") {
			u = i
		}
		if strings.HasPrefix(f, "-D_FORTIFY_SOURCE") {
			d = i
		}
	}
	if u < 0 || d < 0 {
		t.Fatalf("GNUFortify = %v, want both an undefine and a define", GNUFortify)
	}
	if u > d {
		t.Error("-U_FORTIFY_SOURCE must precede -D_FORTIFY_SOURCE, or every file warns about a redefined macro")
	}
}

// The configurations are named rather than expressed as "not Debug", because
// an empty configuration -- a multi-config generator, or no CMAKE_BUILD_TYPE --
// would satisfy "not Debug" and apply _FORTIFY_SOURCE to an unoptimised build,
// which is exactly where it does nothing.
func TestOptimisedConfigsExcludeDebugAndAreExplicit(t *testing.T) {
	if contains(OptimisedConfigs, "Debug") {
		t.Error("Debug is not an optimised configuration; _FORTIFY_SOURCE does nothing at -O0")
	}
	if len(OptimisedConfigs) == 0 {
		t.Fatal("no optimised configurations, so hardening would never apply")
	}
	// RelWithDebInfo is cppm's default release build type, so leaving it out
	// would mean the default release build is the one that is not hardened.
	if !contains(OptimisedConfigs, "RelWithDebInfo") {
		t.Error("RelWithDebInfo is cppm's default release build type and must be hardened")
	}
}

// Control Flow Guard is only enforced if both halves are set: /guard:cf alone
// emits metadata into an image Windows never checks.
func TestControlFlowGuardIsAPair(t *testing.T) {
	if contains(MSVCHardenProbed, "/guard:cf") && !contains(MSVCHardenLinkProbed, "/GUARD:CF") {
		t.Error("/guard:cf without /GUARD:CF at link time produces an image Windows does not enforce")
	}
}

// Flags that are a hard error on some targets must never be in a list that is
// used unprobed: -fcf-protection and -fstack-clash-protection are rejected
// outright on aarch64, so an Apple M-series or Raspberry Pi build would not
// compile at all.
func TestUnportableFlagsAreOnlyInProbedLists(t *testing.T) {
	unprobed := append(append([]string{}, GNUHardenDefines...), GNUFortify...)
	for _, f := range []string{"-fcf-protection", "-fstack-clash-protection", "-fstack-protector-strong"} {
		if contains(unprobed, f) {
			t.Errorf("%q is applied without a probe; it is a hard error on some architectures", f)
		}
		if !contains(GNUHardenProbed, f) {
			t.Errorf("%q is missing from the probed hardening flags", f)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
