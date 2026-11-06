package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cppm/internal/atomicfile"
	"cppm/internal/cmakegen"
	"cppm/internal/lockfile"
	"cppm/internal/manifest"
	"cppm/internal/project"
	"cppm/internal/toolchain"
)

type initOpts struct {
	std   int
	name  string
	force bool
	lib   bool
}

func cmdInit() *Command {
	build := func() (*flag.FlagSet, *initOpts) {
		o := &initOpts{}
		fs := newFlagSet("init")
		fs.IntVar(&o.std, "std", manifest.DefaultStandard, "C++ standard: 11, 14, 17, 20, 23 or 26")
		fs.StringVar(&o.name, "name", "", "project name (default: the directory name)")
		fs.BoolVar(&o.force, "force", false, "overwrite an existing cppm.json")
		fs.BoolVar(&o.lib, "lib", false, "scaffold a library instead of an executable")
		return fs, o
	}
	return &Command{
		Name:    "init",
		Summary: "create a new C++ project",
		Usage:   "cppm init [<directory>] [flags]",
		Long: `Creates a project that configures and builds immediately.

With no directory, or with ".", initialises the current directory. Otherwise the
directory is created if it does not exist.

Written files:
    cppm.json           what the project wants      (commit this)
    cppm.lock           what will be fetched        (commit this)
    CMakeLists.txt      yours after this point -- cppm never rewrites it
    CMakePresets.json   debug and release presets
    src/main.cpp        a hello-world
    .gitignore
    .cppm/              generated files             (git-ignored)

Existing files are never clobbered; they are reported as kept.`,
		flagSet: func() *flag.FlagSet { fs, _ := build(); return fs },
		Run: func(args []string) error {
			fs, o := build()
			if err := parseArgs(fs, args); err != nil {
				return err
			}
			rest := fs.Args()
			if len(rest) > 1 {
				return fmt.Errorf("expected at most one directory, got %d", len(rest))
			}
			dir := "."
			if len(rest) == 1 {
				dir = rest[0]
			}
			return runInit(dir, o)
		},
	}
}

// nameSanitizer strips characters that are not valid in a CMake target name.
var nameSanitizer = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// sanitizeName turns a directory name into a usable project name, and reports
// whether it had to change anything.
func sanitizeName(raw string) (string, bool) {
	cleaned := nameSanitizer.ReplaceAllString(raw, "_")
	cleaned = strings.Trim(cleaned, "_.-")
	if cleaned == "" {
		return "project", true
	}
	// CMake target names must not begin with a digit.
	if cleaned[0] >= '0' && cleaned[0] <= '9' {
		cleaned = "p" + cleaned
	}
	return cleaned, cleaned != raw
}

func runInit(dir string, o *initOpts) error {
	if !validStandard(o.std) {
		return fmt.Errorf("invalid --std %d: expected one of %v", o.std, manifest.ValidStandards)
	}

	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", root, err)
	}

	manifestPath := filepath.Join(root, manifest.FileName)
	if _, err := os.Stat(manifestPath); err == nil && !o.force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", manifest.FileName)
	}

	name := o.name
	if name == "" {
		var changed bool
		name, changed = sanitizeName(filepath.Base(root))
		if changed {
			fmt.Printf("  note: directory name %q is not a valid target name; using %q\n",
				filepath.Base(root), name)
		}
	}

	m := manifest.New(name, o.std)
	if o.lib {
		m.Targets = map[string]manifest.Target{
			name: {Kind: manifest.KindLibrary, Sources: []string{"src/" + name + ".cpp"}},
		}
	}
	if err := m.Validate(); err != nil {
		return err
	}

	// Prefer Ninja when it is installed. Presets are committed, so this records
	// a per-project choice rather than a per-machine one; `cppm doctor` reports
	// a missing generator.
	generator := "Ninja"
	if !toolchain.Ninja().Found() {
		generator = ""
		fmt.Println("  note: ninja not found; presets will use CMake's default generator")
	}

	presets, err := cmakegen.Presets(m, generator)
	if err != nil {
		return err
	}
	lock := lockfile.New(Version)
	lockBytes, err := lock.Bytes()
	if err != nil {
		return err
	}
	manifestBytes, err := m.Bytes()
	if err != nil {
		return err
	}

	mainSource := m.Targets[name].Sources[0]

	// Files cppm owns and always writes.
	owned := []struct{ rel, content string }{
		{manifest.FileName, string(manifestBytes)},
		{lockfile.FileName, string(lockBytes)},
		{filepath.Join(project.DirName, "generated", "cppm_dependencies.cmake"),
			cmakegen.Dependencies(lock, Version)},
	}
	// Files the user owns; never clobbered once they exist.
	userOwned := []struct{ rel, content string }{
		{"CMakeLists.txt", cmakegen.CMakeLists(m)},
		{"CMakePresets.json", presets},
		{mainSource, cmakegen.MainCPP(name)},
		{".gitignore", cmakegen.GitIgnore()},
		{filepath.Join("include", ".gitkeep"), ""},
	}

	fmt.Printf("creating %s project %q (C++%d) in %s\n\n", kindWord(o.lib), name, o.std, root)

	for _, f := range owned {
		if err := atomicfile.WriteString(filepath.Join(root, f.rel), f.content); err != nil {
			return err
		}
		fmt.Printf("  created  %s\n", f.rel)
	}
	for _, f := range userOwned {
		path := filepath.Join(root, f.rel)
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("  kept     %s (already exists)\n", f.rel)
			continue
		}
		if err := atomicfile.WriteString(path, f.content); err != nil {
			return err
		}
		fmt.Printf("  created  %s\n", f.rel)
	}

	fmt.Print("\nnext:\n")
	if rel, err := filepath.Rel(mustCwd(), root); err == nil && rel != "." {
		fmt.Printf("    cd %s\n", rel)
	}
	fmt.Print("    cppm build\n")
	return nil
}

func kindWord(lib bool) string {
	if lib {
		return "library"
	}
	return "executable"
}

func validStandard(s int) bool {
	for _, v := range manifest.ValidStandards {
		if v == s {
			return true
		}
	}
	return false
}

func mustCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
