// Command cppm starts C++ projects, adds dependencies and builds them.
//
// cppm generates CMake and shells out to cmake, ninja and git; it is not a build
// system and it does not replace CMake. See docs/architecture.md.
package main

import (
	"os"
	"runtime/debug"

	"github.com/mehdi-shokohi/cppm/internal/cli"
)

// version is overridden at release time with -ldflags "-X main.version=..."
var version = ""

func main() {
	if v := chooseVersion(version, moduleVersion()); v != "" {
		cli.Version = v
	}
	os.Exit(cli.Main(os.Args[1:]))
}

// moduleVersion returns the version Go itself recorded for this binary.
//
// Go fills this in for `go install <module>@<tag>` and, because it also reads
// VCS tags, for a build from a clean checkout sitting exactly on a tag -- which
// is what install.sh does when no release asset exists for the platform yet.
// Both are installs of a real release and must not be labelled "-dev"; the
// stamp was there all along and cppm was ignoring it.
func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return info.Main.Version
}

// chooseVersion prefers the release stamp, then what Go recorded, and otherwise
// leaves the compiled-in fallback in place.
//
// "(devel)" is Go's answer when it cannot name the version at all, and carries
// less information than the fallback constant, so it loses. A pseudo-version
// for a commit between tags wins instead: it names the exact commit, which is
// what a lockfile's generated_by field is for.
func chooseVersion(stamp, module string) string {
	if stamp != "" {
		return stamp
	}
	if module != "" && module != "(devel)" {
		return module
	}
	return ""
}
