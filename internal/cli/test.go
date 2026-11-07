package cli

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/project"
	"github.com/mehdi-shokohi/cppm/internal/safety"
	"github.com/mehdi-shokohi/cppm/internal/toolchain"
)

type testOpts struct {
	release  bool
	debug    bool
	filter   string
	jobs     int
	verbose  bool
	list     bool
	noBuild  bool
	failFast bool
	warnings string
	sanitize string
	// valgrind is the single tool resolved from the per-tool flags below.
	valgrind string
	genSupp  bool
	// valgrindFlags holds one bool per tool, because the flags are spelled
	// --memcheck and --helgrind rather than --valgrind=memcheck.
	valgrindFlags map[string]*bool
}

func cmdTest() *Command {
	build := func() (*flag.FlagSet, *testOpts) {
		o := &testOpts{}
		fs := newFlagSet("test")
		fs.BoolVar(&o.debug, "debug", false, "test the debug preset (default)")
		fs.BoolVar(&o.release, "release", false, "test the release preset")
		fs.StringVar(&o.filter, "filter", "", "run only tests whose name matches this regular expression")
		fs.IntVar(&o.jobs, "jobs", 0, "parallel jobs, for both the build and the test run")
		fs.BoolVar(&o.verbose, "verbose", false, "show output from passing tests too")
		fs.BoolVar(&o.list, "list", false, "list the tests without running them")
		fs.BoolVar(&o.noBuild, "no-build", false, "do not build first; test whatever is in the build tree")
		fs.BoolVar(&o.failFast, "fail-fast", false, "stop at the first failing test")
		fs.StringVar(&o.warnings, "warnings", "", warningsFlagHelp)
		fs.StringVar(&o.sanitize, "sanitize", "", sanitizeFlagHelp)
		for _, name := range safety.ValgrindTools {
			tool, _ := safety.FindValgrindTool(name)
			fs.BoolVar(valgrindFlagTarget(o, name), name, false,
				"run the tests under valgrind "+name+" -- "+tool.Summary)
		}
		fs.BoolVar(&o.genSupp, "gen-suppressions", false,
			"with a valgrind tool, print a ready-made "+safety.SuppressionsFile+" entry for every error")
		return fs, o
	}
	return &Command{
		Name:    "test",
		Summary: "build the project and run its tests",
		Usage:   "cppm test [flags]",
		Long: `Builds, then runs the tests through CTest.

test is a thin wrapper over CMake presets, like build -- it runs

    cmake --preset <preset>
    cmake --build --preset <preset>
    ctest --preset <preset>

rather than reimplementing test discovery. Anything registered with add_test()
in your CMakeLists.txt is run, whether cppm scaffolded it or you wrote it.

It builds first on purpose. A test run against a stale build tree reports on
code that is no longer there, which is worse than no result at all. Pass
--no-build when you know the tree is current and want the fastest loop.

    cppm test                      # build the debug preset and run every test
    cppm test --release
    cppm test --filter parser      # only tests whose name matches "parser"
    cppm test --verbose            # output from passing tests as well
    cppm test --list               # what would run
    cppm test --warnings=error     # build the tests with -Werror as well

Under a sanitizer -- recompiles into its own build tree, 5-15x slower:

    cppm test --sanitize=asan+ubsan   # memory errors and undefined behaviour
    cppm test --sanitize=tsan         # data races and lock-order inversions

Under valgrind -- no recompilation, 20-100x slower, needs valgrind installed:

    cppm test --memcheck      leaks, invalid reads/writes, uninitialised values
    cppm test --helgrind      data races AND lock-order violations
    cppm test --drd           lighter thread checker, better with many threads
    cppm test --massif        heap profiler
    cppm test --cachegrind    cache and branch-prediction profiler

Which race checker? Use --sanitize=tsan routinely: it is far faster and has
fewer false positives. Use --helgrind when you cannot recompile, or when you
suspect a lock-ordering deadlock -- it is the thorough one for lock order.

A sanitizer and valgrind cannot be combined. Both intercept the same
allocations and each reports the other's bookkeeping as an error, so the
output means nothing; cppm refuses rather than producing it.

Getting a test suite in the first place:

    cppm init --with-tests                       # in a new project
    cppm add github:catchorg/Catch2@v3.7.1 --dev # a real framework, tests only`,
		flagSet: func() *flag.FlagSet { fs, _ := build(); return fs },
		Run: func(args []string) error {
			fs, o := build()
			if err := parseArgs(fs, args); err != nil {
				return err
			}
			if fs.NArg() > 0 {
				return fmt.Errorf("unexpected argument %q; did you mean --filter %s?", fs.Arg(0), fs.Arg(0))
			}
			if o.release && o.debug {
				return fmt.Errorf("--release and --debug are mutually exclusive")
			}
			// A filter is passed straight to ctest's argv. Anything starting
			// with '-' would be read as an option there, so reject it rather
			// than try to escape it.
			if strings.HasPrefix(o.filter, "-") {
				return fmt.Errorf("--filter %q must not begin with '-'; it is a regular expression matched against test names", o.filter)
			}
			if err := checkWarningsFlag(o.warnings); err != nil {
				return err
			}
			if err := checkSanitizeFlag(o.sanitize); err != nil {
				return err
			}
			if err := o.resolveValgrind(fs); err != nil {
				return err
			}
			if o.sanitize != "" && o.valgrind != "" {
				return fmt.Errorf("--sanitize and --%s cannot be combined.\n"+
					"a sanitizer-instrumented binary under valgrind produces output that means nothing:\n"+
					"the two intercept the same allocations and each reports the other's bookkeeping as\n"+
					"an error. run them as two passes -- 'cppm test --sanitize=%s' and 'cppm test --%s'",
					o.valgrind, o.sanitize, o.valgrind)
			}
			if o.genSupp {
				if o.valgrind == "" {
					return fmt.Errorf("--gen-suppressions only applies to a valgrind run; add --memcheck")
				}
				// A suppression silences a reported error, and a profiler
				// reports none -- there would be nothing to paste anywhere.
				if tool, _ := safety.FindValgrindTool(o.valgrind); !tool.FindsErrors {
					return fmt.Errorf("--gen-suppressions does not apply to --%s, which is a profiler and reports no errors to suppress.\n"+
						"it is for --memcheck, --helgrind and --drd", o.valgrind)
				}
			}
			if o.release && o.sanitize != "" {
				return fmt.Errorf("--release and --sanitize are mutually exclusive: a sanitizer build has its own\n" +
					"build tree and its own flags")
			}
			return runTest(o)
		},
	}
}

func runTest(o *testOpts) error {
	ctest := toolchain.CTest()
	if !ctest.Found() {
		return fmt.Errorf("ctest not found in PATH; it ships with CMake, so this is a broken CMake installation -- run 'cppm doctor'")
	}

	p, cmake, err := openForBuild()
	if err != nil {
		return err
	}
	preset := presetFor(o.sanitize, o.release)

	if o.sanitize != "" {
		san, _ := safety.FindSanitizer(o.sanitize)
		if err := checkInjectedSanitizers(p, san, preset); err != nil {
			return err
		}
	}
	var vg toolchain.Tool
	if o.valgrind != "" {
		vg, err = checkValgrindUsable(p, preset)
		if err != nil {
			return err
		}
	}

	// Configuring is not optional even for --no-build: ctest reads
	// CTestTestfile.cmake out of the build tree, and that file only exists
	// after a configure.
	if err := configure(p, cmake, preset, false, SyncOptions{Warnings: o.warnings}); err != nil {
		return err
	}
	if !o.noBuild && !o.list {
		if err := compile(p, cmake, preset, &buildOpts{jobs: o.jobs}); err != nil {
			return err
		}
	}

	args := []string{"--preset", preset}
	if o.filter != "" {
		args = append(args, "-R", o.filter)
	}
	if o.jobs > 0 {
		args = append(args, "-j", strconv.Itoa(o.jobs))
	}
	if o.verbose {
		args = append(args, "--verbose")
	}
	if o.failFast {
		args = append(args, "--stop-on-failure")
	}
	var tool safety.ValgrindTool
	if o.valgrind != "" {
		tool, _ = safety.FindValgrindTool(o.valgrind)
		args = append(args, valgrindArgs(p, preset, vg, tool, o.genSupp)...)
	}
	if o.list {
		args = append(args, "-N")
		return toolchain.Run(p.Root, ctest.Path, args...)
	}

	// ctest exits 8 with "No tests were found" when a project has none, which
	// reads as a build failure. Say what to do instead.
	if n, known := countTests(p, ctest, preset, o.filter); known && n == 0 {
		return noTestsError(p, o.filter)
	}

	switch {
	case o.valgrind != "":
		// Said before the run, not after: 20-100x slower means a suite that
		// takes a minute takes most of an hour, and a user who was not warned
		// assumes it has hung.
		fmt.Printf("\ntesting (%s) under valgrind %s -- expect 20-100x slower than normal\n", preset, tool.Name)
	case o.sanitize != "":
		fmt.Printf("\ntesting (%s) under %s\n", preset, o.sanitize)
	default:
		fmt.Printf("\ntesting (%s)\n", preset)
	}

	// ThreadSanitizer maps its shadow memory at fixed addresses and a
	// high-entropy-ASLR kernel -- the default on current distributions -- can
	// put the binary where that will not fit. Launching the whole ctest run
	// with randomisation off is inherited by every test process.
	argv := append([]string{ctest.Path}, args...)
	if tsanNeedsFixedAddressSpace(o.sanitize) {
		if prefix := aslrDisablingPrefix(); prefix != nil {
			fmt.Println("  (address-space randomisation disabled for this run; ThreadSanitizer needs a fixed shadow mapping)")
			argv = append(prefix, argv...)
		} else {
			fmt.Printf("  %s\n", tsanASLRHint)
		}
	}

	// Stale results from an earlier run would make the "did anything actually
	// get checked" test below pass on a run that checked nothing.
	if o.valgrind != "" {
		clearValgrindResults(p, preset)
	}

	runErr := toolchain.Run(p.Root, argv[0], argv[1:]...)

	if o.valgrind != "" {
		if !valgrindCheckedSomething(p, preset) {
			return fmt.Errorf("ctest reported no memory-check results, so nothing was actually run under valgrind %s.\n"+
				"this is a cppm bug rather than a problem with your project -- please report it,\n"+
				"with the output above and the version from 'cppm --version'", tool.Name)
		}
		reportValgrind(p, preset, tool)
	}
	if runErr != nil {
		if o.valgrind != "" {
			// A memcheck failure is not a failing assertion, and saying "tests
			// failed" sends the user looking in the wrong place.
			return fmt.Errorf("valgrind %s reported errors, or a test failed; see the output above", tool.Name)
		}
		// ctest has already named every failing test and, thanks to
		// outputOnFailure in the test preset, printed its output. Repeating its
		// exit status on top of that tells the user nothing.
		return errors.New("tests failed; see the output above")
	}
	return nil
}

// countTests asks ctest how many tests the filter selects.
//
// Reported as (n, known) because a parse failure must not be mistaken for an
// empty suite -- being wrong here would block a perfectly good test run.
func countTests(p *project.Project, ctest toolchain.Tool, preset, filter string) (int, bool) {
	args := []string{"--preset", preset, "-N"}
	if filter != "" {
		args = append(args, "-R", filter)
	}
	out, err := toolchain.Capture(p.Root, ctest.Path, args...)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Total Tests:") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Total Tests:")))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// noTestsError distinguishes "this project has no tests at all" from "your
// filter matched none of them", because the remedy is completely different.
func noTestsError(p *project.Project, filter string) error {
	if filter != "" {
		return fmt.Errorf("no test matches --filter %q; run 'cppm test --list' to see the names", filter)
	}
	if tests := p.Manifest.TestTargetNames(); len(tests) > 0 {
		return fmt.Errorf("cppm.json declares the test target(s) %s, but the build tree has no registered test.\n"+
			"add_test() lives in CMakeLists.txt, which cppm does not rewrite after init -- check that it is still there:\n"+
			"    add_test(NAME %s COMMAND %s)",
			strings.Join(tests, ", "), tests[0], tests[0])
	}
	return fmt.Errorf("this project has no tests.\n" +
		"scaffold one in a new project with 'cppm init --with-tests', or add to CMakeLists.txt:\n" +
		"    add_executable(my_tests tests/my_test.cpp)\n" +
		"    add_test(NAME my_tests COMMAND my_tests)\n" +
		"and mark it in cppm.json so dev dependencies reach it:\n" +
		"    \"targets\": { \"my_tests\": { \"kind\": \"executable\", \"sources\": [\"tests/my_test.cpp\"], \"test\": true } }")
}
