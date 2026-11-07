package cli

import (
	"strings"
	"testing"

	"github.com/mehdi-shokohi/cppm/internal/project"
	"github.com/mehdi-shokohi/cppm/internal/safety"
	"github.com/mehdi-shokohi/cppm/internal/toolchain"
)

// --test-dir is required alongside --preset, and leaving it out is a silent
// failure rather than a loud one.
//
// `-T memcheck` is a dashboard action: it reads DartConfiguration.tcl out of
// the current directory, and cppm runs ctest from the project root so that
// --preset can find CMakePresets.json. Without --test-dir, ctest looks for
// DartConfiguration.tcl beside cppm.json, does not find it, prints "No tests
// were found!!!" -- and exits 0. The run then looks like a clean memcheck pass
// with nothing checked at all, which is the worst possible outcome for a tool
// whose job is finding bugs. This was a real bug, found the first time a
// machine with valgrind installed ran it.
func TestValgrindArgsPointCtestAtTheBuildTree(t *testing.T) {
	p := &project.Project{Root: "/tmp/proj"}
	tool, _ := safety.FindValgrindTool(safety.ValMemcheck)
	args := valgrindArgs(p, "debug", toolchain.Tool{Name: "valgrind", Path: "/usr/bin/valgrind"}, tool, false)

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--test-dir "+p.BuildDir("debug")) {
		t.Errorf("no --test-dir pointing at the build tree; ctest will find no DartConfiguration.tcl and exit 0: %v", args)
	}
	if !strings.Contains(joined, "-T memcheck") {
		t.Errorf("the memcheck dashboard action is missing: %v", args)
	}
}

// The command is pinned per run rather than taken from the build tree. A tree
// configured before valgrind was installed has MEMORYCHECK_COMMAND set by the
// CTest module's own find_program, whose NAMES list includes NVIDIA's
// compute-sanitizer -- so on a CUDA machine the run would silently use that,
// with valgrind's options.
func TestValgrindArgsPinTheCommand(t *testing.T) {
	p := &project.Project{Root: "/tmp/proj"}
	tool, _ := safety.FindValgrindTool(safety.ValHelgrind)
	args := valgrindArgs(p, "debug", toolchain.Tool{Name: "valgrind", Path: "/opt/bin/valgrind"}, tool, false)

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "MemoryCheckCommand=/opt/bin/valgrind") {
		t.Errorf("the valgrind path is not pinned for this run: %v", args)
	}
	if !strings.Contains(joined, "MemoryCheckType=Valgrind") {
		t.Errorf("the checker type is not pinned for this run: %v", args)
	}
	// The tool selection has to reach valgrind, or --helgrind runs memcheck.
	if !strings.Contains(joined, "--tool=helgrind") {
		t.Errorf("--helgrind does not select helgrind: %v", args)
	}
	// And --error-exitcode, or CI passes with the bug in it.
	if !strings.Contains(joined, "--error-exitcode=1") {
		t.Errorf("no --error-exitcode=1, so valgrind reports the error and exits 0: %v", args)
	}
}

// --gen-suppressions is appended to the tool's own options rather than
// replacing them, because the suppressions have to be generated for the errors
// the configured run actually reports.
func TestGenSuppressionsAddsToTheToolOptions(t *testing.T) {
	p := &project.Project{Root: "/tmp/proj"}
	tool, _ := safety.FindValgrindTool(safety.ValMemcheck)

	without := strings.Join(valgrindArgs(p, "debug", toolchain.Tool{Path: "/usr/bin/valgrind"}, tool, false), " ")
	with := strings.Join(valgrindArgs(p, "debug", toolchain.Tool{Path: "/usr/bin/valgrind"}, tool, true), " ")

	if strings.Contains(without, safety.GenSuppressionsOption) {
		t.Error("suppression generation must be opt-in")
	}
	if !strings.Contains(with, safety.GenSuppressionsOption) {
		t.Error("--gen-suppressions does not reach valgrind")
	}
	if !strings.Contains(with, "--leak-check=full") {
		t.Error("--gen-suppressions replaced the tool's options instead of adding to them")
	}
}

// Every warning level and sanitizer name cppm documents must pass its own
// validation, and a typo must not.
func TestFlagValidationMatchesWhatIsDocumented(t *testing.T) {
	for _, level := range safety.WarningLevels {
		if err := checkWarningsFlag(level); err != nil {
			t.Errorf("--warnings=%s is documented but rejected: %v", level, err)
		}
	}
	if checkWarningsFlag("pedantic") == nil {
		t.Error("an unknown warning level must be rejected")
	}
	for _, name := range safety.Sanitizers {
		if err := checkSanitizeFlag(name); err != nil {
			t.Errorf("--sanitize=%s is documented but rejected: %v", name, err)
		}
	}
	// The one combination people try, and it must come back with a sentence.
	err := checkSanitizeFlag("asan+tsan")
	if err == nil {
		t.Fatal("--sanitize=asan+tsan must be rejected")
	}
	if !strings.Contains(err.Error(), "cannot be combined") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
}

// A sanitizer selects its own preset, and therefore its own build tree.
func TestPresetForPicksTheSanitizerTree(t *testing.T) {
	if got := presetFor("", false); got != "debug" {
		t.Errorf("presetFor(\"\", false) = %q, want debug", got)
	}
	if got := presetFor("", true); got != "release" {
		t.Errorf("presetFor(\"\", true) = %q, want release", got)
	}
	if got := presetFor(safety.SanThread, false); got != safety.SanThread {
		t.Errorf("presetFor(tsan) = %q, want tsan", got)
	}
}
