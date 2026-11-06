// Command cppm starts C++ projects, adds dependencies and builds them.
//
// cppm generates CMake and shells out to cmake, ninja and git; it is not a build
// system and it does not replace CMake. See docs/architecture.md.
package main

import (
	"os"

	"github.com/mehdi-shokohi/cppm/internal/cli"
)

// version is overridden at release time with -ldflags "-X main.version=..."
var version = ""

func main() {
	if version != "" {
		cli.Version = version
	}
	os.Exit(cli.Main(os.Args[1:]))
}
