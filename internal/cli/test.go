package cli

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/project"
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
	preset := "debug"
	if o.release {
		preset = "release"
	}

	// Configuring is not optional even for --no-build: ctest reads
	// CTestTestfile.cmake out of the build tree, and that file only exists
	// after a configure.
	if err := configure(p, cmake, preset, false); err != nil {
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
	if o.list {
		args = append(args, "-N")
		return toolchain.Run(p.Root, ctest.Path, args...)
	}

	// ctest exits 8 with "No tests were found" when a project has none, which
	// reads as a build failure. Say what to do instead.
	if n, known := countTests(p, ctest, preset, o.filter); known && n == 0 {
		return noTestsError(p, o.filter)
	}

	fmt.Printf("\ntesting (%s)\n", preset)
	if err := toolchain.Run(p.Root, ctest.Path, args...); err != nil {
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
