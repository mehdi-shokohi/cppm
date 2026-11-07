package safety

// Valgrind tool names, as they are spelled in `cppm test --<name>`.
const (
	ValMemcheck   = "memcheck"
	ValHelgrind   = "helgrind"
	ValDRD        = "drd"
	ValMassif     = "massif"
	ValCachegrind = "cachegrind"
)

// ValgrindTools are the tools cppm drives, in the order they appear in help.
var ValgrindTools = []string{ValMemcheck, ValHelgrind, ValDRD, ValMassif, ValCachegrind}

// ValgrindTool is one valgrind tool and the options cppm runs it with.
type ValgrindTool struct {
	// Name is the --<name> flag and the valgrind --tool= value.
	Name string
	// Summary is one line for help text.
	Summary string
	// Options are passed to valgrind through CTest's
	// MemoryCheckCommandOptions.
	Options []string
	// FindsErrors is false for the profilers. CTest reports "0 defects" for
	// those no matter what they produce, so cppm has to say where the output
	// went instead of reporting a clean run.
	FindsErrors bool
	// Output names the files the tool writes into the build tree, for the
	// profilers that produce a report rather than a verdict.
	Output string
}

// errorExitCode is why a valgrind run can fail a test at all.
//
// Without it valgrind reports the error, exits 0, and CTest records a pass:
// the pipeline goes green with a use-after-free in it. It is the single most
// important option here and the easiest to leave out.
const errorExitCode = "--error-exitcode=1"

var valgrindTools = map[string]ValgrindTool{
	ValMemcheck: {
		Name:        ValMemcheck,
		Summary:     "leaks, invalid reads and writes, uninitialised values",
		FindsErrors: true,
		Options: []string{
			"--tool=memcheck",
			"--leak-check=full",
			// Without this, "still reachable" blocks are not shown and a real
			// leak hides among them.
			"--show-leak-kinds=all",
			// Reports where an uninitialised value came from, not just where
			// it was used. Roughly halves the speed and is worth it: the use
			// site alone is rarely enough to find the bug.
			"--track-origins=yes",
			errorExitCode,
		},
	},
	ValHelgrind: {
		Name:        ValHelgrind,
		Summary:     "data races and lock-order violations, with no recompilation",
		FindsErrors: true,
		Options: []string{
			"--tool=helgrind",
			// Helgrind's lock-order report is its advantage over TSan, and
			// this is what prints both orders rather than one.
			"--history-level=full",
			errorExitCode,
		},
	},
	ValDRD: {
		Name:        ValDRD,
		Summary:     "lighter thread checker; better than helgrind with many threads",
		FindsErrors: true,
		Options: []string{
			"--tool=drd",
			errorExitCode,
		},
	},
	ValMassif: {
		Name:        ValMassif,
		Summary:     "heap profiler: what allocated the memory, and when",
		FindsErrors: false,
		Output:      "massif.out.<pid>",
		Options: []string{
			"--tool=massif",
			// Without this only the heap is accounted for, and a program whose
			// problem is stack depth looks fine.
			"--stacks=yes",
		},
	},
	ValCachegrind: {
		Name:        ValCachegrind,
		Summary:     "cache and branch-prediction profiler",
		FindsErrors: false,
		Output:      "cachegrind.out.<pid>",
		Options: []string{
			"--tool=cachegrind",
			"--branch-sim=yes",
		},
	},
}

// FindValgrindTool returns the named tool.
func FindValgrindTool(name string) (ValgrindTool, bool) {
	t, ok := valgrindTools[name]
	return t, ok
}

// GenSuppressionsOption asks valgrind to print a suppression entry for every
// error it reports, ready to paste into valgrind.supp.
//
// Writing a suppression by hand means getting valgrind's own stack-frame
// syntax right, which almost nobody does on the first try.
const GenSuppressionsOption = "--gen-suppressions=all"

// SuppressionsFile is the name cppm scaffolds and wires in. A suppressions
// file is almost always needed: a stock glibc, GTK or CUDA produces memcheck
// output of its own that is not the user's to fix.
const SuppressionsFile = "valgrind.supp"
