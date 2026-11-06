package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/atomicfile"
	"github.com/mehdi-shokohi/cppm/internal/cmakegen"
	"github.com/mehdi-shokohi/cppm/internal/lockfile"
	"github.com/mehdi-shokohi/cppm/internal/manifest"
	"github.com/mehdi-shokohi/cppm/internal/toolchain"
)

type initOpts struct {
	std       int
	name      string
	force     bool
	lib       bool
	withTests bool
	compiler  string
	generator string
}

func cmdInit() *Command {
	build := func() (*flag.FlagSet, *initOpts) {
		o := &initOpts{}
		fs := newFlagSet("init")
		fs.IntVar(&o.std, "std", manifest.DefaultStandard, "C++ standard: 11, 14, 17, 20, 23 or 26")
		fs.StringVar(&o.name, "name", "", "project name (default: the directory name)")
		fs.BoolVar(&o.force, "force", false, "overwrite an existing cppm.json")
		fs.BoolVar(&o.lib, "lib", false, "scaffold a library instead of an executable")
		fs.BoolVar(&o.withTests, "with-tests", false, "also scaffold a tests/ directory and a first test")
		fs.StringVar(&o.compiler, "compiler", "", "C++ compiler: clang++, g++, or a path (default: CMake's choice)")
		fs.StringVar(&o.generator, "generator", "", "CMake generator (default: Ninja when installed)")
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

With --with-tests, also:
    tests/<name>_test.cpp   a first test, registered with CTest
    a test target in cppm.json marked "test": true, and add_test() wiring

The scaffolded test uses no framework, so it compiles and runs offline before
any dependency exists. Add a real one when you want it:

    cppm add github:catchorg/Catch2@v3.7.1 --dev

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
	testName, testSource := "", ""
	if o.withTests {
		testName = name + "_tests"
		testSource = "tests/" + name + "_test.cpp"
		m.Targets[testName] = manifest.Target{
			Kind:    manifest.KindExecutable,
			Sources: []string{testSource},
			Test:    true,
		}
	}
	if err := m.Validate(); err != nil {
		return err
	}

	// Compiler and generator are written out explicitly, at their detected
	// values, rather than left absent. CMAKE_CXX_COMPILER has to be set before
	// project() runs, so these are rendered into CMakePresets.json; and a
	// setting the user cannot see in cppm.json is one they cannot change.
	switch {
	case o.compiler != "":
		cxx, changed := manifest.NormalizeCXX(o.compiler)
		if changed {
			fmt.Printf("  note: %q compiles C++ but does not link the C++ standard library; using %q\n",
				o.compiler, cxx)
		}
		m.Toolchain.CXX = cxx
	default:
		if detected := toolchain.Compiler(); detected.Found() {
			m.Toolchain.CXX = detected.Name
		}
	}
	if m.Toolchain.CXX != "" {
		m.Toolchain.C = manifest.CompanionC(m.Toolchain.CXX)
	}

	switch {
	case o.generator != "":
		m.Toolchain.Generator = o.generator
	case toolchain.Ninja().Found():
		m.Toolchain.Generator = "Ninja"
	default:
		fmt.Println("  note: ninja not found; presets will use CMake's default generator")
	}

	if err := m.Validate(); err != nil {
		return err
	}

	presets, err := cmakegen.Presets(m)
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
		{cmakegen.GeneratedRelPath, cmakegen.Managed(m, lock, Version)},
		{"CMakePresets.json", presets},
	}
	// Files the user owns; never clobbered once they exist.
	userOwned := []struct{ rel, content string }{
		{"CMakeLists.txt", cmakegen.CMakeLists(m)},
		{mainSource, cmakegen.MainCPP(name)},
		{".gitignore", cmakegen.GitIgnore()},
		{filepath.Join("include", ".gitkeep"), ""},
	}
	if o.withTests {
		userOwned = append(userOwned,
			struct{ rel, content string }{testSource, cmakegen.TestMainCPP(name)})
	}

	desc := fmt.Sprintf("C++%d", o.std)
	if m.Toolchain.CXX != "" {
		desc += ", " + m.Toolchain.CXX
	}
	if m.Toolchain.Generator != "" {
		desc += ", " + m.Toolchain.Generator
	}
	fmt.Printf("creating %s project %q (%s) in %s\n\n", kindWord(o.lib), name, desc, root)

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
	if o.withTests {
		fmt.Print("    cppm test\n")
	}
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
