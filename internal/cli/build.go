package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"cppm/internal/project"
	"cppm/internal/toolchain"
)

type buildOpts struct {
	release bool
	debug   bool
	clean   bool
	jobs    int
	target  string
	verbose bool
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
CLion and Visual Studio read exactly the configuration the CLI uses.`,
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
			return runBuild(o)
		},
	}
}

func runBuild(o *buildOpts) error {
	cmake := toolchain.CMake()
	if !cmake.Found() {
		return fmt.Errorf("cmake not found in PATH; run 'cppm doctor' for details")
	}

	p, err := project.Open(mustCwd())
	if err != nil {
		return err
	}

	preset := "debug"
	if o.release {
		preset = "release"
	}

	if _, err := os.Stat(p.PresetsPath()); err != nil {
		return fmt.Errorf("CMakePresets.json is missing from %s; was this project created by cppm init?", p.Root)
	}

	if _, err := Sync(p); err != nil {
		return err
	}

	buildDir := p.BuildDir(preset)
	if o.clean {
		fmt.Printf("removing %s\n", relTo(p.Root, buildDir))
		if err := os.RemoveAll(buildDir); err != nil {
			return fmt.Errorf("clean %s: %w", buildDir, err)
		}
	}

	fmt.Printf("configuring (%s)\n", preset)
	if err := toolchain.Run(p.Root, cmake.Path, "--preset", preset); err != nil {
		return err
	}

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
	if err := toolchain.Run(p.Root, cmake.Path, args...); err != nil {
		return err
	}

	fmt.Printf("\nbuilt %s -> %s\n", p.Manifest.Name, relTo(p.Root, buildDir))
	return nil
}

// relTo renders path relative to base when that is shorter and clearer.
func relTo(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil && len(rel) < len(path) {
		return rel
	}
	return path
}
