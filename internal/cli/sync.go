package cli

import (
	"flag"
	"fmt"

	"cppm/internal/atomicfile"
	"cppm/internal/cmakegen"
	"cppm/internal/lockfile"
	"cppm/internal/project"
)

func cmdSync() *Command {
	build := func() (*flag.FlagSet, *bool) {
		quiet := false
		fs := newFlagSet("sync")
		fs.BoolVar(&quiet, "quiet", false, "only report problems")
		return fs, &quiet
	}
	return &Command{
		Name:    "sync",
		Summary: "regenerate the CMake files from cppm.json and cppm.lock",
		Usage:   "cppm sync [flags]",
		Long: `Brings .cppm/generated up to date with the manifest and lockfile.

sync never changes a pin. If the lockfile already resolves a dependency, that
resolution is obeyed exactly; sync only fills gaps and rewrites generated CMake.
Only 'cppm update' moves pins. Getting this wrong would destroy reproducibility,
so it is covered by a test.

sync is idempotent: running it twice produces byte-identical output.`,
		flagSet: func() *flag.FlagSet { fs, _ := build(); return fs },
		Run: func(args []string) error {
			fs, quiet := build()
			if err := parseArgs(fs, args); err != nil {
				return err
			}
			if fs.NArg() > 0 {
				return fmt.Errorf("unexpected argument %q", fs.Arg(0))
			}
			p, err := project.Open(mustCwd())
			if err != nil {
				return err
			}
			changed, err := Sync(p)
			if err != nil {
				return err
			}
			if !*quiet {
				n := len(p.Manifest.Dependencies)
				if changed {
					fmt.Printf("synced %d dependency(ies); regenerated %s\n", n, relTo(p.Root, p.DepsCMakePath()))
				} else {
					fmt.Printf("already up to date (%d dependency(ies))\n", n)
				}
			}
			return nil
		},
	}
}

// Sync regenerates the managed CMake file and reports whether anything changed.
//
// Exported because `build` runs it first, so that building a freshly cloned
// project works without a separate sync step.
func Sync(p *project.Project) (bool, error) {
	if err := reconcile(p); err != nil {
		return false, err
	}
	content := cmakegen.Dependencies(p.Lock, Version)
	path := p.DepsCMakePath()

	if same, err := fileHasContent(path, content); err != nil {
		return false, err
	} else if same {
		return false, nil
	}
	if err := atomicfile.WriteString(path, content); err != nil {
		return false, err
	}
	return true, nil
}

// reconcile drops locked packages the manifest no longer declares, and reports
// manifest entries that have never been resolved.
//
// It deliberately does not resolve anything itself: resolution needs the network
// and belongs to `add` and `update`.
func reconcile(p *project.Project) error {
	for _, name := range p.Lock.Names() {
		if _, ok := p.Manifest.Dependencies[name]; !ok {
			p.Lock.Remove(name)
		}
	}

	var unresolved []string
	for _, name := range p.Manifest.DependencyNames() {
		dep := p.Manifest.Dependencies[name]
		locked, ok := p.Lock.Find(name)

		if !ok {
			// Path dependencies need no network resolution, so they can be
			// locked here; git dependencies cannot.
			if dep.Source == "path" {
				p.Lock.Put(lockfile.Package{
					Name:         name,
					Source:       dep.Source,
					Path:         dep.Path,
					Targets:      dep.Targets,
					TargetOrigin: originFor(dep.Targets),
					Options:      dep.Options,
				})
				continue
			}
			unresolved = append(unresolved, name)
			continue
		}

		// An explicit target list in the manifest always wins over whatever was
		// inferred earlier.
		if len(dep.Targets) > 0 {
			locked.Targets = dep.Targets
			locked.TargetOrigin = lockfile.OriginExplicit
		}
		locked.Options = dep.Options
		if dep.Source == "path" {
			locked.Path = dep.Path
		}
	}

	if len(unresolved) > 0 {
		return fmt.Errorf("these dependencies are in %s but not resolved in %s: %v\n"+
			"run 'cppm add' for them again, or 'cppm update' to resolve",
			"cppm.json", "cppm.lock", unresolved)
	}
	if err := p.SaveLock(); err != nil {
		return err
	}
	return nil
}

func originFor(targets []string) string {
	if len(targets) > 0 {
		return lockfile.OriginExplicit
	}
	return lockfile.OriginUnresolved
}
