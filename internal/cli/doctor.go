package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/cmakegen"
	"github.com/mehdi-shokohi/cppm/internal/manifest"
	"github.com/mehdi-shokohi/cppm/internal/project"
	"github.com/mehdi-shokohi/cppm/internal/safety"
	"github.com/mehdi-shokohi/cppm/internal/toolchain"
)

type check struct {
	name   string
	status string // ok, warn, fail
	detail string
	hint   string
}

const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

func cmdDoctor() *Command {
	return &Command{
		Name:    "doctor",
		Summary: "diagnose the toolchain and the project",
		Usage:   "cppm doctor",
		Long: `Checks the tools cppm drives and, if run inside a project, the project itself.

Exits non-zero if anything failed, so it is usable in CI.`,
		Run: func(args []string) error {
			fs := newFlagSet("doctor")
			if err := parseArgs(fs, args); err != nil {
				return err
			}
			checks := append(toolchainChecks(), projectChecks()...)

			width := 0
			for _, c := range checks {
				if len(c.name) > width {
					width = len(c.name)
				}
			}
			var failed, warned int
			for _, c := range checks {
				mark := "+"
				switch c.status {
				case statusWarn:
					mark, warned = "!", warned+1
				case statusFail:
					mark, failed = "x", failed+1
				}
				fmt.Printf("  %s %-*s  %s\n", mark, width, c.name, c.detail)
				if c.hint != "" && c.status != statusOK {
					fmt.Printf("    %*s  -> %s\n", width, "", c.hint)
				}
			}

			fmt.Printf("\n%d ok, %d warning(s), %d failure(s)\n", len(checks)-failed-warned, warned, failed)
			if failed > 0 {
				return fmt.Errorf("%d check(s) failed", failed)
			}
			return nil
		},
	}
}

func toolchainChecks() []check {
	var out []check

	cmake := toolchain.CMake()
	switch {
	case !cmake.Found():
		out = append(out, check{"cmake", statusFail, "not found in PATH",
			"install CMake " + manifest.DefaultCMakeMinimum + " or newer"})
	case cmake.Version != "" && toolchain.CompareVersions(cmake.Version, manifest.DefaultCMakeMinimum) < 0:
		out = append(out, check{"cmake", statusFail,
			fmt.Sprintf("%s at %s (too old)", cmake.Version, cmake.Path),
			"cppm generates projects requiring CMake " + manifest.DefaultCMakeMinimum + " or newer"})
	default:
		out = append(out, check{"cmake", statusOK, fmt.Sprintf("%s at %s", cmake.Version, cmake.Path), ""})
	}

	if ninja := toolchain.Ninja(); ninja.Found() {
		out = append(out, check{"ninja", statusOK, fmt.Sprintf("%s at %s", ninja.Version, ninja.Path), ""})
	} else {
		out = append(out, check{"ninja", statusWarn, "not found in PATH",
			"optional, but much faster than Make; projects created without it use CMake's default generator"})
	}

	if git := toolchain.Git(); git.Found() {
		out = append(out, check{"git", statusOK, fmt.Sprintf("%s at %s", git.Version, git.Path), ""})
	} else {
		out = append(out, check{"git", statusFail, "not found in PATH",
			"required to fetch dependencies; CMake's FetchContent shells out to git too"})
	}

	// Optional, and reported as such: valgrind is effectively Linux-only, so a
	// macOS or Windows user needs to be told what to use instead rather than
	// just that it is absent.
	if vg := toolchain.Valgrind(); vg.Found() {
		out = append(out, check{"valgrind", statusOK, fmt.Sprintf("%s at %s", vg.Version, vg.Path), ""})
	} else {
		out = append(out, check{"valgrind", statusWarn, "not found in PATH", valgrindDoctorHint()})
	}

	if cc := toolchain.Compiler(); cc.Found() {
		out = append(out, check{"c++ compiler", statusOK, fmt.Sprintf("%s %s at %s", cc.Name, cc.Version, cc.Path), ""})
	} else {
		out = append(out, check{"c++ compiler", statusFail, "no c++, g++ or clang++ in PATH",
			"install a C++ toolchain, or set CXX to your compiler"})
	}
	return out
}

// valgrindDoctorHint is the one-line form of the install hint.
//
// One line because the hint column is indented to the width of the longest
// check name, which a wrapped string cannot know.
func valgrindDoctorHint() string {
	switch {
	case isWindows():
		return "valgrind does not run on Windows; use 'cppm test --sanitize=asan+ubsan' and '--sanitize=tsan'"
	case isDarwin():
		return "valgrind is broken on Apple Silicon; use 'cppm test --sanitize=asan+ubsan' and '--sanitize=tsan'"
	default:
		return "optional: 'apt install valgrind' enables 'cppm test --memcheck' and '--helgrind'"
	}
}

// toolchainRequestChecks validates the compiler and generator the project asks
// for, as opposed to what happens to be installed.
func toolchainRequestChecks(p *project.Project) []check {
	var out []check
	tc := p.Manifest.Toolchain

	if tc.CXX == "" {
		out = append(out, check{"toolchain.cxx", statusOK, "not set; CMake picks the compiler", ""})
	} else if found := toolchain.Find(tc.CXX); found.Found() {
		out = append(out, check{"toolchain.cxx", statusOK,
			fmt.Sprintf("%s %s at %s", tc.CXX, found.Version, found.Path), ""})
	} else {
		out = append(out, check{"toolchain.cxx", statusFail,
			fmt.Sprintf("%q is requested in cppm.json but not found", tc.CXX),
			"install it, or change \"toolchain\".\"cxx\" and run 'cppm sync'"})
	}

	// Invoking "clang" or "gcc" as the C++ compiler compiles but fails to link
	// the standard library, which surfaces as a wall of undefined references.
	if tc.CXX != "" {
		if fixed, changed := manifest.NormalizeCXX(tc.CXX); changed {
			out = append(out, check{"toolchain.cxx", statusWarn,
				fmt.Sprintf("%q is a C driver, not a C++ driver", tc.CXX),
				fmt.Sprintf("it will not link the C++ standard library; use %q instead", fixed)})
		}
	}

	// Ask the compiler whether it supports the requested standard, rather than
	// letting CMake fail at generate time with an error that never mentions
	// cppm.json.
	cxx := tc.CXX
	if cxx == "" {
		cxx = toolchain.Compiler().Name
	}
	if cxx != "" {
		if supported, known := toolchain.StandardSupport(cxx, p.Manifest.CXX.Standard); known && !supported {
			out = append(out, check{"cxx standard", statusFail,
				fmt.Sprintf("%s does not support C++%d", cxx, p.Manifest.CXX.Standard),
				fmt.Sprintf("lower \"cxx\".\"standard\" in cppm.json and run 'cppm sync', or install a newer %s", cxx)})
		} else if known {
			out = append(out, check{"cxx standard", statusOK,
				fmt.Sprintf("C++%d, supported by %s", p.Manifest.CXX.Standard, cxx), ""})
		}
	}

	if tc.Generator != "" {
		if tc.Generator == "Ninja" && !toolchain.Ninja().Found() {
			out = append(out, check{"toolchain.generator", statusFail,
				"Ninja is requested in cppm.json but not installed",
				"install ninja, or change \"toolchain\".\"generator\" and run 'cppm sync'"})
		} else {
			out = append(out, check{"toolchain.generator", statusOK, tc.Generator, ""})
		}
	}
	return out
}

// driftChecks reports settings that cppm cannot apply by regenerating, because
// they live in the user-owned CMakeLists.txt. project() has to be called there,
// so cppm can only detect the mismatch and say so.
func driftChecks(p *project.Project) []check {
	b, err := os.ReadFile(p.CMakeListsPath())
	if err != nil {
		return nil
	}
	content := string(b)
	var out []check

	if name := cmakeProjectName(content); name != "" && name != p.Manifest.Name {
		out = append(out, check{"project name", statusWarn,
			fmt.Sprintf("cppm.json says %q, CMakeLists.txt says %q", p.Manifest.Name, name),
			"project() must be called in CMakeLists.txt, so cppm cannot change it; edit it there"})
	}

	// A standard set in CMakeLists.txt after the include would silently override
	// cppm.json, making 'cppm sync' appear to do nothing.
	if idx := strings.Index(content, "CMAKE_CXX_STANDARD"); idx >= 0 {
		incIdx := strings.Index(content, cmakegen.GeneratedRelPath)
		if incIdx >= 0 && idx > incIdx {
			out = append(out, check{"cxx standard", statusWarn,
				"CMakeLists.txt sets CMAKE_CXX_STANDARD after the cppm include",
				"that overrides cppm.json, so 'cppm sync' will not change the standard; remove it to let cppm.json win"})
		}
	}
	return out
}

// cmakeProjectName extracts the first argument of project().
func cmakeProjectName(content string) string {
	idx := strings.Index(content, "project(")
	if idx < 0 {
		return ""
	}
	rest := content[idx+len("project("):]
	if end := strings.IndexAny(rest, " \t\r\n)"); end >= 0 {
		return strings.TrimSpace(rest[:end])
	}
	return ""
}

// safetyCheck reports the warning level and hardening the project builds with.
//
// Reported rather than judged, with one exception. "How hard is this project
// being compiled" is otherwise only answerable by reading the generated CMake,
// and a project that quietly has its warnings off is the state this whole
// feature exists to prevent.
func safetyCheck(p *project.Project) check {
	level := p.Manifest.WarningLevel()
	hardening := "hardening off"
	if p.Manifest.Harden() {
		hardening = "hardening on"
	}
	detail := fmt.Sprintf("warnings %s, %s", level, hardening)

	// The hints are one line each on purpose: the hint column is indented to
	// the width of the longest check name, which is not known here, so a
	// wrapped hint would align correctly only by coincidence.
	if level == safety.WarningsOff {
		return check{"safety", statusWarn, detail,
			"the compiler is reporting nothing; try 'cppm build --warnings=default'"}
	}
	// -Werror committed to cppm.json means a new compiler release can stop
	// this project building without a line of it changing. Worth saying once,
	// not worth failing over -- it is a legitimate choice for a project that
	// pins its compiler.
	if level == safety.WarningsError {
		return check{"safety", statusWarn, detail,
			"a new compiler release can now stop this project building; prefer \"strict\" in cppm.json and --warnings=error in CI"}
	}
	return check{"safety", statusOK, detail, ""}
}

func projectChecks() []check {
	p, err := project.Open(mustCwd())
	if err != nil {
		return []check{{"project", statusWarn, "not inside a cppm project",
			"run 'cppm init' to create one; toolchain checks above still apply"}}
	}

	out := []check{{"project", statusOK,
		fmt.Sprintf("%s %s (C++%d) at %s", p.Manifest.Name, p.Manifest.Version, p.Manifest.CXX.Standard, p.Root), ""}}

	// The lockfile must cover every declared dependency, or a build will fail
	// at configure time rather than here.
	var missing []string
	for _, n := range p.Manifest.DependencyNames() {
		if _, ok := p.Lock.Find(n); !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		out = append(out, check{"lockfile", statusFail,
			fmt.Sprintf("%d dependency(ies) not resolved: %s", len(missing), strings.Join(missing, ", ")),
			"run 'cppm sync'"})
	} else {
		out = append(out, check{"lockfile", statusOK,
			fmt.Sprintf("%d dependency(ies) resolved", len(p.Lock.Packages)), ""})
	}

	if _, err := os.Stat(p.ManagedCMakePath()); err != nil {
		out = append(out, check{"generated cmake", statusFail, "missing", "run 'cppm sync'"})
	} else {
		out = append(out, check{"generated cmake", statusOK, relTo(p.Root, p.ManagedCMakePath()), ""})
	}

	out = append(out, safetyCheck(p))

	// CMAKE_CXX_EXTENSIONS defaults to ON in CMake, which silently compiles the
	// project as gnu++NN and leaves dependencies on mixed dialects. CMakeLists
	// is user-owned, so cppm can only warn.
	if b, err := os.ReadFile(p.CMakeListsPath()); err == nil {
		if !strings.Contains(string(b), "CMAKE_CXX_EXTENSIONS") {
			out = append(out, check{"cxx extensions", statusWarn,
				"CMAKE_CXX_EXTENSIONS is not set in CMakeLists.txt",
				"it defaults to ON, so your code compiles as gnu++NN; add set(CMAKE_CXX_EXTENSIONS OFF)"})
		} else {
			out = append(out, check{"cxx extensions", statusOK, "explicitly set in CMakeLists.txt", ""})
		}
	} else {
		out = append(out, check{"CMakeLists.txt", statusFail, "missing", "run 'cppm init --force' to regenerate project files"})
	}

	if pb, err := os.ReadFile(p.PresetsPath()); err != nil {
		out = append(out, check{"presets", statusWarn, "CMakePresets.json missing",
			"'cppm build' needs it; run 'cppm sync' to regenerate"})
	} else if !cmakegen.PresetsAreCppmGenerated(pb) {
		out = append(out, check{"presets", statusWarn,
			"CMakePresets.json has no cppm vendor stamp",
			"cppm will not overwrite it, so toolchain changes in cppm.json will not reach the build"})
	} else {
		out = append(out, check{"presets", statusOK, "CMakePresets.json (cppm-generated)", ""})
	}

	out = append(out, testingChecks(p)...)
	out = append(out, toolchainRequestChecks(p)...)
	out = append(out, driftChecks(p)...)
	return out
}

// testingChecks reports a test setup that is declared but not wired up.
//
// Both halves live in files cppm cannot reconcile on its own: the "test": true
// declaration is in cppm.json, and the add_test() call is in the user-owned
// CMakeLists.txt. When they disagree the symptom is a dev dependency that is
// fetched and linked to nothing, or a test that never runs -- neither of which
// produces an error anywhere.
func testingChecks(p *project.Project) []check {
	var out []check

	dev := p.Manifest.DevDependencyNames()
	tests := p.Manifest.TestTargetNames()

	if len(dev) > 0 && len(tests) == 0 {
		out = append(out, check{"dev dependencies", statusWarn,
			fmt.Sprintf("%s are dev-only, but no target is marked \"test\": true", strings.Join(dev, ", ")),
			"they are fetched and linked to nothing; mark a test target in cppm.json and run 'cppm sync'"})
		return out
	}
	if len(tests) == 0 {
		return nil
	}

	// add_test() lives in CMakeLists.txt, so cppm can only look.
	b, err := os.ReadFile(p.CMakeListsPath())
	if err != nil {
		return out
	}
	var unregistered []string
	for _, name := range tests {
		if !strings.Contains(string(b), "add_test(NAME "+name+" ") {
			unregistered = append(unregistered, name)
		}
	}
	if len(unregistered) > 0 {
		out = append(out, check{"test targets", statusWarn,
			fmt.Sprintf("%s declared in cppm.json but not registered with CTest", strings.Join(unregistered, ", ")),
			fmt.Sprintf("add to CMakeLists.txt: add_test(NAME %s COMMAND %s)", unregistered[0], unregistered[0])})
	} else {
		detail := fmt.Sprintf("%s registered with CTest", strings.Join(tests, ", "))
		if len(dev) > 0 {
			detail += fmt.Sprintf("; dev dependencies %s link there", strings.Join(dev, ", "))
		}
		out = append(out, check{"test targets", statusOK, detail, ""})
	}
	return out
}
