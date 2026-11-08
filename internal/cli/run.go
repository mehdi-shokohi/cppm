package cli

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/manifest"
	"github.com/mehdi-shokohi/cppm/internal/project"
	"github.com/mehdi-shokohi/cppm/internal/safety"
	"github.com/mehdi-shokohi/cppm/internal/toolchain"
)

type runOpts struct {
	release  bool
	debug    bool
	jobs     int
	target   string
	verbose  bool
	noBuild  bool
	warnings string
	sanitize string
	valgrindChoice
	// args is the program's own argv, everything after `--`.
	args []string
}

func cmdRun() *Command {
	build := func() (*flag.FlagSet, *runOpts) {
		o := &runOpts{}
		fs := newFlagSet("run")
		fs.BoolVar(&o.debug, "debug", false, "build and run the debug preset (default)")
		fs.BoolVar(&o.release, "release", false, "build and run the release preset")
		fs.IntVar(&o.jobs, "jobs", 0, "parallel jobs for the build")
		fs.StringVar(&o.target, "target", "", "which executable to run (default: the project's only one)")
		fs.BoolVar(&o.verbose, "verbose", false, "show the compiler command lines")
		fs.BoolVar(&o.noBuild, "no-build", false, "do not build first; run whatever is in the build tree")
		fs.StringVar(&o.warnings, "warnings", "", warningsFlagHelp)
		fs.StringVar(&o.sanitize, "sanitize", "", sanitizeFlagHelp)
		o.registerValgrindFlags(fs, "run the program under valgrind ")
		return fs, o
	}
	return &Command{
		Name:    "run",
		Summary: "build the project and run it",
		Usage:   "cppm run [flags] [-- program arguments]",
		Long: `Builds, then runs the project's executable.

Everything after -- is passed to your program untouched:

    cppm run                        # build the debug preset and run it
    cppm run --release
    cppm run -- --config a.toml -v  # your program's own arguments
    cppm run --target tools_cli     # when the project has several executables

The program runs with the project root as its working directory, so a relative
path in your program means the same thing here as it does when you run the
binary by hand. cppm exits with your program's exit status.

Checking the real program, with no test suite involved:

    cppm run --sanitize=asan+ubsan   # memory errors and undefined behaviour
    cppm run --sanitize=tsan         # data races
    cppm run --memcheck              # leaks and invalid accesses, no rebuild
    cppm run --helgrind              # data races and lock-order violations
    cppm run --massif                # heap profiler

This is the counterpart to 'cppm test --sanitize=...' and exists because the
sanitizers instrument every line the compiler sees, including main(), so they
never needed a test suite to be useful -- only a way to be launched. Use it to
chase down a crash or a leak in the real program.

What it does NOT replace: a run only inspects the code paths you actually
execute. Driving the program by hand checks whatever you happened to do in
that session and cannot be repeated in CI. 'cppm test --sanitize=...' checks
the same instrumentation against a suite that runs the same way every time.
Use run to find a bug and test to keep it from coming back.

--sanitize sets the runtime options that make a sanitizer stop at the first
error instead of printing and carrying on, which is the difference between a
failed command and a clean-looking one. A variable you have exported yourself
is appended after cppm's, so it wins.`,
		flagSet: func() *flag.FlagSet { fs, _ := build(); return fs },
		Run: func(args []string) error {
			cppmArgs, progArgs, sawSeparator := splitProgramArgs(args)
			fs, o := build()
			if err := parseArgs(fs, cppmArgs); err != nil {
				return err
			}
			o.args = progArgs
			// A bare positional is ambiguous -- `cppm run foo` reads as both
			// "run the target foo" and "pass foo to the program" -- so it is
			// refused rather than guessed at.
			if fs.NArg() > 0 {
				arg := fs.Arg(0)
				if !sawSeparator {
					return fmt.Errorf("unexpected argument %q.\n"+
						"to choose which executable to run:   cppm run --target %s\n"+
						"to pass it to your program:          cppm run -- %s", arg, arg, arg)
				}
				return fmt.Errorf("unexpected argument %q", arg)
			}
			if o.release && o.debug {
				return fmt.Errorf("--release and --debug are mutually exclusive")
			}
			if strings.HasPrefix(o.target, "-") {
				return fmt.Errorf("--target %q must not begin with '-'; it is the name of a target in cppm.json", o.target)
			}
			if err := checkWarningsFlag(o.warnings); err != nil {
				return err
			}
			if err := checkSanitizeFlag(o.sanitize); err != nil {
				return err
			}
			if err := o.resolveValgrind(); err != nil {
				return err
			}
			if err := o.checkValgrindFlags("run", o.sanitize); err != nil {
				return err
			}
			if o.release && o.sanitize != "" {
				return fmt.Errorf("--release and --sanitize are mutually exclusive: a sanitizer build has its own\n" +
					"build tree and its own flags (-g -O1), chosen so stack traces stay readable")
			}
			return runRun(o)
		},
	}
}

// splitProgramArgs divides cppm's own arguments from the program's.
//
// Done before the flag package sees them, not by reading fs.Args(): parseArgs
// collects positionals from either side of `--` into one list, so by then
// `cppm run x -- y` and `cppm run -- x y` are indistinguishable. The first form
// has to be an error and the second has to be passed through, so the split
// happens here where the separator is still visible.
func splitProgramArgs(args []string) (cppmArgs, progArgs []string, sawSeparator bool) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:], true
		}
	}
	return args, nil, false
}

func runRun(o *runOpts) error {
	p, cmake, err := openForBuild()
	if err != nil {
		return err
	}
	target, err := runTargetOf(p.Manifest, o.target)
	if err != nil {
		return err
	}
	preset := presetFor(o.sanitize, o.release)

	var san safety.Sanitizer
	if o.sanitize != "" {
		san, _ = safety.FindSanitizer(o.sanitize)
		if err := checkInjectedSanitizers(p, san, preset); err != nil {
			return err
		}
	}
	// Checked before the build rather than after, so a run that cannot produce
	// a meaningful result costs nothing. checkValgrindUsable also refuses to
	// point valgrind at a sanitizer-instrumented tree, which it reads off
	// CMakeCache.txt rather than off the flags -- the binaries on disk are what
	// valgrind would actually run.
	var vg toolchain.Tool
	if o.valgrind != "" {
		vg, err = checkValgrindUsable(p, preset)
		if err != nil {
			return err
		}
	}

	if !o.noBuild {
		if err := configure(p, cmake, preset, false, SyncOptions{Warnings: o.warnings}); err != nil {
			return err
		}
		// Everything, not just the chosen target: a cppm.json target name that
		// has drifted from the CMakeLists.txt one would fail here as
		// "ninja: unknown target", which tells a cppm user nothing. Building
		// all of it leaves the mismatch to be reported by executablePath,
		// which can say what to do about it.
		if err := compile(p, cmake, preset, &buildOpts{jobs: o.jobs, verbose: o.verbose}); err != nil {
			return err
		}
	}

	exe, err := executablePath(p, preset, target)
	if err != nil {
		return err
	}

	argv := []string{exe}
	argv = append(argv, o.args...)
	var tool safety.ValgrindTool
	if o.valgrind != "" {
		tool, _ = safety.FindValgrindTool(o.valgrind)
		// valgrind's own options first, then the program and its arguments:
		// everything after the executable belongs to the program, which is why
		// a program argument starting with '-' needs no special handling.
		argv = append(append([]string{vg.Path}, valgrindRunArgs(p, tool, o.genSupp)...), argv...)
	}

	var env []string
	if o.sanitize != "" {
		env = sanitizerEnv(san)
	}

	// ThreadSanitizer maps its shadow memory at fixed addresses and a
	// high-entropy-ASLR kernel -- the default on current distributions -- can
	// put the binary where that will not fit, killing it before main().
	if tsanNeedsFixedAddressSpace(o.sanitize) {
		if prefix := aslrDisablingPrefix(); prefix != nil {
			fmt.Println("  (address-space randomisation disabled for this run; ThreadSanitizer needs a fixed shadow mapping)")
			argv = append(prefix, argv...)
		} else {
			fmt.Printf("  %s\n", tsanASLRHint)
		}
	}

	announceRun(p, preset, exe, tool, o)

	status, err := toolchain.RunProgram(p.Root, env, argv[0], argv[1:]...)
	if err != nil {
		return err
	}
	if o.valgrind != "" && !tool.FindsErrors {
		reportProfilerOutput(p, tool)
	}
	return runResult(status, tool, o)
}

// announceRun says what is about to happen, before it happens.
//
// Said in advance because valgrind is 20-100x slower: a user who was not told
// assumes the program has hung.
func announceRun(p *project.Project, preset, exe string, tool safety.ValgrindTool, o *runOpts) {
	switch {
	case o.valgrind != "":
		fmt.Printf("\nrunning %s under valgrind %s -- expect 20-100x slower than normal\n",
			relTo(p.Root, exe), tool.Name)
	case o.sanitize != "":
		fmt.Printf("\nrunning %s under %s\n", relTo(p.Root, exe), o.sanitize)
	default:
		fmt.Printf("\nrunning %s (%s)\n", relTo(p.Root, exe), preset)
	}
}

// runResult turns the program's exit status into cppm's.
//
// A program's own non-zero exit is not a cppm failure and gets no message: it
// has already said whatever it wanted to say, and cppm's job is to pass the
// status through so a script sees what it would have seen running the binary
// directly. The cases that do get a message are the ones where the status needs
// interpreting.
func runResult(status toolchain.ExitStatus, tool safety.ValgrindTool, o *runOpts) error {
	if status.OK() {
		return nil
	}
	switch {
	case o.valgrind != "" && tool.FindsErrors:
		// Ambiguous by construction: --error-exitcode=1 makes valgrind exit 1
		// on a finding, but valgrind otherwise passes the program's own status
		// through. Saying both is honest; claiming either alone would not be.
		return &exitStatusError{code: 1, msg: fmt.Sprintf(
			"valgrind %s reported errors, or the program itself failed (%s); see the output above",
			tool.Name, status.Describe())}
	case o.sanitize != "" && status.Signal != "":
		// The expected shape of a sanitizer finding, and worth naming: a bare
		// "aborted" reads as a crash in the program rather than as the
		// sanitizer doing its job.
		return &exitStatusError{code: 1, msg: fmt.Sprintf(
			"%s stopped the program (%s).\n"+
				"that is what a sanitizer finding looks like: cppm sets abort_on_error=1, so the\n"+
				"first error ends the run. The report above names the file and line",
			o.sanitize, status.Signal)}
	case o.sanitize != "":
		return &exitStatusError{code: 1, msg: fmt.Sprintf(
			"the program failed (%s) under %s; see the output above", status.Describe(), o.sanitize)}
	case status.Signal != "":
		// A bare crash is the one case where cppm has something useful to add,
		// because the remedy is another cppm command away.
		return &exitStatusError{code: 1, msg: fmt.Sprintf(
			"the program crashed (%s).\nto find out where:  cppm run --sanitize=asan+ubsan%s\n"+
				"or, without rebuilding:  cppm run --memcheck%s",
			status.Signal, programArgsSuffix(o.args), programArgsSuffix(o.args))}
	default:
		return &exitStatusError{code: status.Code}
	}
}

// programArgsSuffix re-renders the program's arguments so a suggested command
// can be retyped as it stands.
func programArgsSuffix(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return " -- " + strings.Join(args, " ")
}

// sanitizerEnv renders a sanitizer's runtime options as KEY=VALUE.
//
// These are the options the test preset sets through CMakePresets.json, which
// nothing applies to a binary run directly -- and without them a sanitizer
// reports its finding and carries on to exit 0, which is the silent-pass
// failure this whole area exists to avoid.
//
// A value the user has exported is appended after cppm's rather than replacing
// it or being dropped: the sanitizers read these colon-separated lists left to
// right with the last setting winning, so cppm's defaults apply unless the user
// deliberately asked for something else.
func sanitizerEnv(san safety.Sanitizer) []string {
	out := make([]string, 0, len(san.Env))
	for _, k := range sortedEnvKeys(san.Env) {
		v := san.Env[k]
		if existing := os.Getenv(k); existing != "" {
			v += ":" + existing
		}
		out = append(out, k+"="+v)
	}
	return out
}

// valgrindRunArgs builds valgrind's own options for a direct run.
//
// Much shorter than the `cppm test` path: there is no CTest dashboard action
// here, so no DartConfiguration.tcl, no --overwrite, and no need to check
// afterwards that anything was actually examined -- cppm hands valgrind the
// binary itself and valgrind's exit status is the result.
func valgrindRunArgs(p *project.Project, tool safety.ValgrindTool, genSupp bool) []string {
	opts := append([]string{}, tool.Options...)
	if genSupp {
		opts = append(opts, safety.GenSuppressionsOption)
	}
	if supp := filepath.Join(p.Root, safety.SuppressionsFile); fileExists(supp) {
		opts = append(opts, "--suppressions="+supp)
	}
	return opts
}

// reportProfilerOutput says where a profiler's output went.
//
// A profiler writes no verdict, so a successful run otherwise looks like it did
// nothing. The files land in the working directory, which is the project root.
func reportProfilerOutput(p *project.Project, tool safety.ValgrindTool) {
	fmt.Printf("\n%s writes no verdict, only a profile. Look for %s in\n    %s\n",
		tool.Name, tool.Output, p.Root)
	switch tool.Name {
	case safety.ValMassif:
		fmt.Println("read it with:  ms_print massif.out.<pid>")
	case safety.ValCachegrind:
		fmt.Println("read it with:  cg_annotate cachegrind.out.<pid>")
	}
}

// runTargetOf picks which executable to run.
//
// Declining rather than guessing when the answer is not unambiguous: running
// the wrong binary wastes a 20-100x valgrind run, and a tool that picks for you
// teaches you to stop reading what it picked. A test target is never chosen
// automatically -- it has to be named -- for the same reason cppm never infers
// that a target called "foo_tests" is a test.
func runTargetOf(m *manifest.Manifest, want string) (string, error) {
	if want != "" {
		t, ok := m.Targets[want]
		if !ok {
			return "", fmt.Errorf("cppm.json declares no target %q.\n"+
				"the targets it does declare: %s", want, strings.Join(m.TargetNames(), ", "))
		}
		if t.Kind != manifest.KindExecutable {
			return "", fmt.Errorf("target %q is a %s, and a library cannot be run.\n"+
				"name an executable instead: %s", want, t.Kind, listOrNone(executableNames(m, false)))
		}
		return want, nil
	}

	candidates := executableNames(m, false)
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		if tests := executableNames(m, true); len(tests) > 0 {
			return "", fmt.Errorf("this project has no executable other than its test target(s).\n"+
				"run the tests with 'cppm test', or name one explicitly:  cppm run --target %s", tests[0])
		}
		return "", fmt.Errorf("this project has no executable target, so there is nothing to run.\n" +
			"it builds a library; add an executable to CMakeLists.txt and declare it in cppm.json:\n" +
			"    \"targets\": { \"my_tool\": { \"kind\": \"executable\", \"sources\": [\"src/main.cpp\"] } }")
	default:
		return "", fmt.Errorf("this project has %d executables, so cppm will not choose one for you.\n"+
			"pick one:  cppm run --target %s\nthe others: %s",
			len(candidates), candidates[0], strings.Join(candidates[1:], ", "))
	}
}

// executableNames lists executable targets, either the test ones or the rest,
// in the manifest's deterministic order.
func executableNames(m *manifest.Manifest, tests bool) []string {
	var out []string
	for _, n := range m.TargetNames() {
		t := m.Targets[n]
		if t.Kind == manifest.KindExecutable && t.Test == tests {
			out = append(out, n)
		}
	}
	return out
}

func listOrNone(names []string) string {
	if len(names) == 0 {
		return "(this project declares none)"
	}
	return strings.Join(names, ", ")
}

// executablePath finds the binary a preset's build tree produced.
//
// The two well-known layouts are tried by name first, then the tree is searched
// for an exact filename match, because a CMakeLists.txt is free to set
// RUNTIME_OUTPUT_DIRECTORY and cppm does not rewrite that file. The search
// skips _deps, where a dependency may well have built a binary with the same
// name as the user's.
func executablePath(p *project.Project, preset, target string) (string, error) {
	dir := p.BuildDir(preset)
	name := exeFileName(target)
	candidates := []string{
		filepath.Join(dir, name),
		// Multi-config generators (Visual Studio, Xcode) ignore
		// CMAKE_BUILD_TYPE and put each configuration in its own subdirectory.
		filepath.Join(dir, configurationOf(p, preset), name),
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c, nil
		}
	}

	found := searchForExecutable(dir, name)
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		if _, err := os.Stat(dir); err != nil {
			return "", fmt.Errorf("there is no build tree at %s yet, so there is nothing to run.\n"+
				"drop --no-build, or run '%s' first", relTo(p.Root, dir), buildCommandFor(preset))
		}
		return "", fmt.Errorf("%s contains no executable named %q.\n"+
			"if the build succeeded, the add_executable() name in CMakeLists.txt and the target name\n"+
			"in cppm.json have drifted apart -- make them match.\n"+
			"cppm also cannot know where a RUNTIME_OUTPUT_DIRECTORY sends the binary: CMakeLists.txt\n"+
			"is yours and cppm does not read build logic out of it",
			relTo(p.Root, dir), name)
	default:
		rel := make([]string, 0, len(found))
		for _, f := range found {
			rel = append(rel, relTo(p.Root, f))
		}
		return "", fmt.Errorf("found %d files named %q in %s, so cppm cannot tell which one to run:\n    %s",
			len(found), name, relTo(p.Root, dir), strings.Join(rel, "\n    "))
	}
}

// searchForExecutable looks through a build tree for an exact filename match.
func searchForExecutable(dir, name string) []string {
	var found []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			// CMakeFiles holds the compiler-detection binaries, and _deps holds
			// the dependencies' own build trees: a match in either is not the
			// user's program.
			case "CMakeFiles", "_deps", "Testing":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == name {
			found = append(found, path)
		}
		return nil
	})
	return found
}

// configurationOf names the build configuration a preset builds, for locating
// output under a multi-config generator.
func configurationOf(p *project.Project, preset string) string {
	if bt := p.Manifest.CMake.BuildTypeFor(preset); bt != "" {
		return bt
	}
	// A sanitizer preset deliberately sets no CMAKE_BUILD_TYPE -- a build
	// type's -O would overrule the -O1 the sanitizer flags ask for -- so its
	// build preset names Debug purely for the multi-config generators.
	return "Debug"
}

// buildCommandFor renders the `cppm build` invocation that fills a given
// preset's tree, so a suggested command can be retyped as it stands.
func buildCommandFor(preset string) string {
	switch {
	case safety.ValidSanitizer(preset):
		return "cppm build --sanitize=" + preset
	case preset == "release":
		return "cppm build --release"
	default:
		return "cppm build"
	}
}

// exeFileName adds the extension Windows needs.
func exeFileName(target string) string {
	if isWindows() {
		return target + ".exe"
	}
	return target
}
