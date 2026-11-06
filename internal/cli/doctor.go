package cli

import (
	"fmt"
	"os"
	"strings"

	"cppm/internal/manifest"
	"cppm/internal/project"
	"cppm/internal/toolchain"
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

	if cc := toolchain.Compiler(); cc.Found() {
		out = append(out, check{"c++ compiler", statusOK, fmt.Sprintf("%s %s at %s", cc.Name, cc.Version, cc.Path), ""})
	} else {
		out = append(out, check{"c++ compiler", statusFail, "no c++, g++ or clang++ in PATH",
			"install a C++ toolchain, or set CXX to your compiler"})
	}
	return out
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

	if _, err := os.Stat(p.DepsCMakePath()); err != nil {
		out = append(out, check{"generated cmake", statusFail, "missing", "run 'cppm sync'"})
	} else {
		out = append(out, check{"generated cmake", statusOK, relTo(p.Root, p.DepsCMakePath()), ""})
	}

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

	if _, err := os.Stat(p.PresetsPath()); err != nil {
		out = append(out, check{"presets", statusWarn, "CMakePresets.json missing",
			"'cppm build' needs it; run 'cppm init --force'"})
	} else {
		out = append(out, check{"presets", statusOK, "CMakePresets.json", ""})
	}
	return out
}
