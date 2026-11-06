package cli

import (
	"flag"
	"os"
	"strings"
)

// fileHasContent reports whether path already holds exactly want.
//
// Used to keep sync idempotent: an unchanged generated file is left untouched,
// so its mtime does not move and CMake does not re-configure for no reason.
func fileHasContent(path, want string) (bool, error) {
	got, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return string(got) == want, nil
}

// parseArgs parses flags that may appear before OR after positional arguments.
//
// Go's flag package stops at the first non-flag argument, so plain fs.Parse
// would silently ignore the flags in `cppm init MyApp --std 17` -- which is
// exactly how people type it. This permutes flags ahead of positionals first,
// consulting the flag set to tell whether a flag consumes the next argument.
//
// Everything after a literal "--" is treated as positional.
func parseArgs(fs *flag.FlagSet, args []string) error {
	var flagArgs, positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]

		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) > 1 && strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			name := strings.TrimLeft(a, "-")
			// "-flag=value" carries its own value; "-flag value" does not, and
			// only non-boolean flags consume the following argument.
			if !strings.Contains(name, "=") {
				if f := fs.Lookup(name); f != nil && !isBoolFlag(f.Value) && i+1 < len(args) {
					flagArgs = append(flagArgs, args[i+1])
					i++
				}
			}
			continue
		}
		positional = append(positional, a)
	}

	reordered := append(flagArgs, "--")
	reordered = append(reordered, positional...)
	return fs.Parse(reordered)
}

// isBoolFlag reports whether a flag value is boolean, and so takes no argument.
func isBoolFlag(v flag.Value) bool {
	b, ok := v.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}
