package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/project"
	"github.com/mehdi-shokohi/cppm/internal/safety"
	"github.com/mehdi-shokohi/cppm/internal/toolchain"
)

type buildOpts struct {
	release  bool
	debug    bool
	clean    bool
	jobs     int
	target   string
	verbose  bool
	warnings string
	sanitize string
}

func cmdBuild() *Command {
	build := func() (*flag.FlagSet, *buildOpts) {
		o := &buildOpts{}
		fs := newFlagSet("build")
		fs.BoolVar(&o.debug, "debug", false, "build the debug preset (default)")
		fs.BoolVar(&o.release, "release", false, "build the release preset")
		fs.BoolVar(&o.clean, "clean", false, "delete the build directory first")
		fs.IntVar(&o.jobs, "jobs", 0, "parallel jobs (default: let the backend decide)")
		fs.StringVar(&o.target, "target", "", "build only this target")
		fs.BoolVar(&o.verbose, "verbose", false, "show the compiler command lines")
		fs.StringVar(&o.warnings, "warnings", "", warningsFlagHelp)
		fs.StringVar(&o.sanitize, "sanitize", "", sanitizeFlagHelp)
		return fs, o
	}
	return &Command{
		Name:    "build",
		Summary: "configure and build the project",
		Usage:   "cppm build [flags]",
		Long: `Syncs, configures and builds.

build is a thin wrapper over CMake presets -- it runs

    cmake --preset <preset>
    cmake --build --preset <preset>

rather than reimplementing CMake invocation. That means VS Code CMake Tools,
CLion and Visual Studio read exactly the configuration the CLI uses.

Warnings come from "safety" in cppm.json and are applied to your targets only,
never to the dependencies. --warnings tries a level out without committing to
it:

    cppm build --warnings=strict    # the flags that find real bugs
    cppm build --warnings=error     # strict plus -Werror, for a CI stage
    cppm build --warnings=off       # the compiler's defaults

To keep a level, put it in cppm.json instead and run 'cppm sync':

    "safety": { "warnings": "strict", "hardening": true }

--sanitize builds under a sanitizer, in a build tree of its own:

    cppm build --sanitize=asan+ubsan   # the usual pairing
    cppm build --sanitize=tsan         # data races and lock-order inversions

Unlike the warnings, sanitizer flags deliberately DO reach the dependencies:
ASan and TSan instrument the whole program or they are unreliable. Because
cppm builds every dependency from source in the same tree, that works here
without a custom toolchain.

Run the result with 'cppm test --sanitize=<name>', which also sets the
runtime options (halt_on_error and the rest) that make a sanitizer failure
fail the test rather than print and carry on.`,
		flagSet: func() *flag.FlagSet { fs, _ := build(); return fs },
		Run: func(args []string) error {
			fs, o := build()
			if err := parseArgs(fs, args); err != nil {
				return err
			}
			if fs.NArg() > 0 {
				return fmt.Errorf("unexpected argument %q", fs.Arg(0))
			}
			if o.release && o.debug {
				return fmt.Errorf("--release and --debug are mutually exclusive")
			}
			if err := checkWarningsFlag(o.warnings); err != nil {
				return err
			}
			if err := checkSanitizeFlag(o.sanitize); err != nil {
				return err
			}
			// A sanitizer selects its own preset, so --release has nothing to
			// apply to and silently ignoring it would produce an unoptimised
			// build the user believes is a release one.
			if o.sanitize != "" && o.release {
				return fmt.Errorf("--release and --sanitize are mutually exclusive: a sanitizer build has its own\n" +
					"build tree and its own flags (-g -O1), chosen so stack traces stay readable")
			}
			return runBuild(o)
		},
	}
}

func runBuild(o *buildOpts) error {
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
	if err := configure(p, cmake, preset, o.clean, SyncOptions{Warnings: o.warnings}); err != nil {
		return err
	}
	if err := compile(p, cmake, preset, o); err != nil {
		return err
	}
	fmt.Printf("\nbuilt %s -> %s\n", p.Manifest.Name, relTo(p.Root, p.BuildDir(preset)))
	if o.sanitize != "" {
		// The runtime options live in the test preset, so a binary run by hand
		// gets the sanitizer's defaults -- which report and carry on. Said
		// here because a sanitizer run that does not stop on the first error
		// is easy to mistake for a clean one.
		san, _ := safety.FindSanitizer(o.sanitize)
		fmt.Printf("\nrun it with 'cppm test --sanitize=%s', which sets:\n", o.sanitize)
		for _, k := range sortedEnvKeys(san.Env) {
			fmt.Printf("    %s=%s\n", k, san.Env[k])
		}
		if tsanNeedsFixedAddressSpace(o.sanitize) {
			// Running the binary directly will die before main() with
			// "unexpected memory mapping" on any kernel with high-entropy
			// ASLR, which is most of them now.
			fmt.Printf("\nto run it by hand, disable address-space randomisation --\n")
			fmt.Printf("ThreadSanitizer needs a fixed shadow mapping:\n")
			fmt.Printf("    setarch -R %s\n", relTo(p.Root, filepath.Join(p.BuildDir(preset), p.Manifest.Name)))
		}
	}
	return nil
}

// sortedEnvKeys keeps the printed environment deterministic.
func sortedEnvKeys(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k := range env {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// openForBuild is the common entry for every command that drives CMake.
func openForBuild() (*project.Project, toolchain.Tool, error) {
	cmake := toolchain.CMake()
	if !cmake.Found() {
		return nil, cmake, fmt.Errorf("cmake not found in PATH; run 'cppm doctor' for details")
	}
	p, err := project.Open(mustCwd())
	if err != nil {
		return nil, cmake, err
	}
	if _, err := os.Stat(p.PresetsPath()); err != nil {
		return nil, cmake, fmt.Errorf("CMakePresets.json is missing from %s; was this project created by cppm init?", p.Root)
	}
	return p, cmake, nil
}

// configure syncs the generated CMake and configures the build tree.
//
// Shared by build and test so that a test run can never execute against a tree
// configured from an older cppm.json.
func configure(p *project.Project, cmake toolchain.Tool, preset string, clean bool, so SyncOptions) error {
	changes, err := SyncWith(p, so)
	if err != nil {
		return err
	}
	for _, c := range changes {
		fmt.Printf("  %s\n", c)
	}
	if so.Warnings != "" {
		// Said out loud because the generated CMake now disagrees with
		// cppm.json, and the next plain 'cppm build' will put it back.
		fmt.Printf("  warnings: %s (from --warnings, not saved to cppm.json)\n", so.Warnings)
	}

	buildDir := p.BuildDir(preset)

	// CMake stores the compiler in CMakeCache.txt and refuses to configure if it
	// changes, with an error that does not explain what to do. Since cppm.json
	// is the source of truth, detect the mismatch and start the build tree
	// afresh -- build output is disposable and git-ignored.
	if stale, why := staleToolchain(p, buildDir); stale {
		fmt.Printf("  %s; reconfiguring from scratch\n", why)
		if err := os.RemoveAll(buildDir); err != nil {
			return fmt.Errorf("remove %s: %w", buildDir, err)
		}
	}

	if clean {
		fmt.Printf("removing %s\n", relTo(p.Root, buildDir))
		if err := os.RemoveAll(buildDir); err != nil {
			return fmt.Errorf("clean %s: %w", buildDir, err)
		}
	}

	fmt.Printf("configuring (%s)\n", preset)
	return toolchain.Run(p.Root, cmake.Path, "--preset", preset)
}

// compile runs the build preset.
func compile(p *project.Project, cmake toolchain.Tool, preset string, o *buildOpts) error {
	fmt.Printf("\nbuilding (%s)\n", preset)
	args := []string{"--build", "--preset", preset}
	if o.jobs > 0 {
		args = append(args, "--parallel", strconv.Itoa(o.jobs))
	}
	if o.target != "" {
		args = append(args, "--target", o.target)
	}
	if o.verbose {
		args = append(args, "--verbose")
	}
	return toolchain.Run(p.Root, cmake.Path, args...)
}

// staleToolchain reports whether an existing build tree was configured with a
// different compiler or generator than cppm.json now asks for.
func staleToolchain(p *project.Project, buildDir string) (bool, string) {
	cachePath := filepath.Join(buildDir, "CMakeCache.txt")
	b, err := os.ReadFile(cachePath)
	if err != nil {
		return false, "" // no build tree yet, nothing to invalidate
	}
	cache := string(b)

	if want := p.Manifest.Toolchain.CXX; want != "" {
		if got := cacheValue(cache, "CMAKE_CXX_COMPILER"); got != "" && !sameProgram(got, want) {
			return true, fmt.Sprintf("compiler changed to %s (build tree used %s)", want, got)
		}
	}
	if want := p.Manifest.Toolchain.Generator; want != "" {
		if got := cacheValue(cache, "CMAKE_GENERATOR"); got != "" && got != want {
			return true, fmt.Sprintf("generator changed to %s (build tree used %s)", want, got)
		}
	}
	return false, ""
}

// cacheValue reads a CMakeCache.txt entry, whose lines look like
// NAME:TYPE=value.
func cacheValue(cache, name string) string {
	for _, line := range strings.Split(cache, "\n") {
		if !strings.HasPrefix(line, name+":") {
			continue
		}
		if i := strings.Index(line, "="); i >= 0 {
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}

// sameProgram compares a cached absolute compiler path with what the manifest
// asks for, which may be a bare command name resolved through PATH.
func sameProgram(cached, wanted string) bool {
	if cached == wanted {
		return true
	}
	if resolved, err := exec.LookPath(wanted); err == nil {
		if resolved == cached {
			return true
		}
		// Follow symlinks, so /usr/bin/c++ -> g++ does not look like a change.
		if a, err1 := filepath.EvalSymlinks(resolved); err1 == nil {
			if bPath, err2 := filepath.EvalSymlinks(cached); err2 == nil && a == bPath {
				return true
			}
		}
	}
	return filepath.Base(cached) == filepath.Base(wanted)
}

// relTo renders path relative to base when that is shorter and clearer.
func relTo(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil && len(rel) < len(path) {
		return rel
	}
	return path
}

// warningsFlagHelp is shared by build and test so the two cannot describe the
// same flag differently.
var warningsFlagHelp = "warning level for this build only: " +
	strings.Join(safety.WarningLevels, ", ") + " (default: \"safety\".warnings in cppm.json)"

// checkWarningsFlag validates --warnings before anything is configured.
//
// Named levels are checked here rather than passed through to the generated
// CMake, because a typo would otherwise become a project that builds with the
// warnings the user thought they had turned on.
func checkWarningsFlag(level string) error {
	if level == "" || safety.ValidWarnings(level) {
		return nil
	}
	return fmt.Errorf("--warnings %q is not a level cppm knows; expected one of %s\n"+
		"  off      the compiler's defaults\n"+
		"  default  -Wall -Wextra\n"+
		"  strict   the warnings that find real bugs, and need some cleaning up first\n"+
		"  error    strict plus -Werror -- meant for CI, where a new compiler release\n"+
		"           introducing a warning should fail a pipeline, not your laptop",
		level, strings.Join(safety.WarningLevels, ", "))
}
