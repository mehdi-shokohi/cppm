package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/project"
	"github.com/mehdi-shokohi/cppm/internal/safety"
	"github.com/mehdi-shokohi/cppm/internal/toolchain"
)

// valgrindFlagTarget returns the bool each --<tool> flag writes into.
//
// One bool per tool rather than a single --valgrind=<tool> because the roadmap
// spells them as `cppm test --helgrind`, and because a tool name is not a value
// anybody wants to quote. resolveValgrind folds them back into one choice.
func valgrindFlagTarget(o *testOpts, name string) *bool {
	if o.valgrindFlags == nil {
		o.valgrindFlags = map[string]*bool{}
	}
	b := new(bool)
	o.valgrindFlags[name] = b
	return b
}

// resolveValgrind collapses the per-tool flags into one choice and rejects two
// at once.
//
// Two tools in one run cannot be honoured: valgrind takes a single --tool, and
// running the suite twice under cppm's own control would report two sets of
// results as one. Refusing is the only honest option.
func (o *testOpts) resolveValgrind(fs *flag.FlagSet) error {
	var chosen []string
	for _, name := range safety.ValgrindTools {
		if b, ok := o.valgrindFlags[name]; ok && *b {
			chosen = append(chosen, name)
		}
	}
	switch len(chosen) {
	case 0:
		return nil
	case 1:
		o.valgrind = chosen[0]
		return nil
	default:
		return fmt.Errorf("--%s cannot be combined: valgrind runs one tool at a time.\n"+
			"run them as separate passes", strings.Join(chosen, " and --"))
	}
}

// checkValgrindUsable refuses a valgrind run that cannot produce a meaningful
// result, before anything is built.
func checkValgrindUsable(p *project.Project, preset string) (toolchain.Tool, error) {
	// An instrumented build tree is the combination the roadmap says cppm must
	// refuse. Checked against the tree rather than the flags, because the tree
	// may have been configured by an earlier `cppm build --sanitize=asan` and
	// the binaries on disk are what valgrind would actually run.
	if san := sanitizerOfBuildTree(p, preset); san != "" {
		return toolchain.Tool{}, fmt.Errorf("%s was configured with --sanitize=%s, and a sanitizer-instrumented binary\n"+
			"under valgrind produces output that means nothing -- the two intercept the same\n"+
			"allocations and each reports the other's bookkeeping as an error.\n"+
			"run valgrind against an uninstrumented tree: 'cppm test --memcheck' uses build/debug",
			relTo(p.Root, p.BuildDir(preset)), san)
	}

	vg := toolchain.Valgrind()
	if !vg.Found() {
		return vg, fmt.Errorf("valgrind not found in PATH.\n%s", valgrindInstallHint())
	}
	return vg, nil
}

// valgrindInstallHint says how to get valgrind, and what to use where it does
// not exist.
//
// Valgrind is effectively Linux-only: it is absent on Windows and broken on
// Apple Silicon. Saying "not found" without saying that leaves a macOS user
// trying to install something that will not work.
func valgrindInstallHint() string {
	switch {
	case isWindows():
		return "valgrind does not run on Windows. Use 'cppm test --sanitize=asan+ubsan' for memory\n" +
			"errors and 'cppm test --sanitize=tsan' for data races; between them they cover most of\n" +
			"what memcheck and helgrind find."
	case isDarwin():
		return "valgrind is unreliable on macOS and does not work at all on Apple Silicon.\n" +
			"Use 'cppm test --sanitize=asan+ubsan' and 'cppm test --sanitize=tsan' instead, which\n" +
			"are supported there and cover most of the same ground."
	default:
		return "install it:  apt install valgrind  |  dnf install valgrind  |  pacman -S valgrind\n" +
			"or use 'cppm test --sanitize=asan+ubsan' and '--sanitize=tsan', which need no extra tool."
	}
}

// valgrindArgs builds the ctest arguments for a valgrind run.
//
// CTest's own memcheck support rather than a launcher of cppm's own: the
// generated CMake sets MEMORYCHECK_COMMAND and include(CTest) writes it into
// DartConfiguration.tcl, which is the only thing `ctest -T memcheck` reads.
// The tool is selected per run with --overwrite, so the default in the build
// tree stays memcheck for anybody running ctest by hand.
func valgrindArgs(p *project.Project, preset string, vg toolchain.Tool, tool safety.ValgrindTool, genSupp bool) []string {
	opts := append([]string{}, tool.Options...)
	if genSupp {
		opts = append(opts, safety.GenSuppressionsOption)
	}
	args := []string{
		"-T", "memcheck",
		// --test-dir as well as --preset, and this is not redundant.
		// `-T memcheck` is a dashboard action: it reads DartConfiguration.tcl
		// out of the *current* directory, and cppm runs ctest from the project
		// root so that --preset can find CMakePresets.json. Without this,
		// ctest looks for DartConfiguration.tcl beside cppm.json, does not
		// find it, reports "No tests were found!!!" -- and exits 0, so the run
		// looks like a clean memcheck pass with nothing checked at all.
		"--test-dir", p.BuildDir(preset),
		// The command is pinned for this run, not taken from the build tree.
		// A tree configured before valgrind was installed has
		// MEMORYCHECK_COMMAND set by the CTest module's own find_program,
		// whose NAMES list includes NVIDIA's compute-sanitizer -- so on a
		// machine with the CUDA toolkit the run would silently use that
		// instead, with valgrind's options.
		"--overwrite", "MemoryCheckCommand=" + vg.Path,
		"--overwrite", "MemoryCheckType=Valgrind",
		"--overwrite", "MemoryCheckCommandOptions=" + strings.Join(opts, " "),
	}
	// Passed explicitly as well as set in the cache, because --overwrite
	// replaces the options wholesale and the suppressions file is a separate
	// setting that a stale build tree may not carry.
	if supp := filepath.Join(p.Root, safety.SuppressionsFile); fileExists(supp) {
		args = append(args, "--overwrite", "MemoryCheckSuppressionFile="+supp)
	}
	return args
}

// valgrindCheckedSomething reports whether the run actually put a test under
// valgrind.
//
// ctest exits 0 when its memcheck action finds no tests, so a misconfigured run
// is otherwise indistinguishable from a clean one -- the worst outcome for a
// tool whose whole job is to find bugs. This happened for real during
// development: without --test-dir, ctest looked for DartConfiguration.tcl
// beside cppm.json, said "No tests were found!!!", and exited 0.
//
// Checked by looking for the per-test log ctest writes rather than by matching
// its console wording, because the log is the thing that has to exist for the
// result to mean anything -- and it is written for the profilers too, not just
// for memcheck.
func valgrindCheckedSomething(p *project.Project, preset string) bool {
	matches, err := filepath.Glob(filepath.Join(p.BuildDir(preset), "Testing", "Temporary", "MemoryChecker.*.log"))
	return err == nil && len(matches) > 0
}

// reportValgrind explains the result of a run that CTest cannot judge.
//
// CTest counts "defects" by parsing memcheck's output format, so a helgrind or
// DRD run is reported with a defect count that means little, and a profiler run
// is always reported as clean. Saying where the output went is more useful than
// repeating a zero.
func reportValgrind(p *project.Project, preset string, tool safety.ValgrindTool) {
	dir := p.BuildDir(preset)
	if !tool.FindsErrors {
		fmt.Printf("\n%s writes no verdict, only a profile. Look for %s in\n    %s\n",
			tool.Name, tool.Output, relTo(p.Root, dir))
		switch tool.Name {
		case safety.ValMassif:
			fmt.Println("read it with:  ms_print massif.out.<pid>")
		case safety.ValCachegrind:
			fmt.Println("read it with:  cg_annotate cachegrind.out.<pid>")
		}
		return
	}
	// The XML CTest leaves behind carries the full output, which is where the
	// actual stack traces are; the console summary is a count.
	logs := filepath.Join(dir, "Testing", "Temporary", "MemoryChecker.*.log")
	fmt.Printf("\nfull %s output, with the stack traces, is in\n    %s\n", tool.Name, relTo(p.Root, logs))
	if tool.Name == safety.ValMemcheck {
		fmt.Printf("to silence errors from system libraries, run again with --gen-suppressions and\n" +
			"paste what it prints into " + safety.SuppressionsFile + "\n")
	}
}

// clearValgrindResults removes the per-test logs from a previous run.
//
// Without this, valgrindCheckedSomething would be satisfied by yesterday's
// results and would pass a run that checked nothing today.
func clearValgrindResults(p *project.Project, preset string) {
	matches, err := filepath.Glob(filepath.Join(p.BuildDir(preset), "Testing", "Temporary", "MemoryChecker.*.log"))
	if err != nil {
		return
	}
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
