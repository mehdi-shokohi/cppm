package cli

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/atomicfile"
	"github.com/mehdi-shokohi/cppm/internal/cmakegen"
	"github.com/mehdi-shokohi/cppm/internal/lockfile"
	"github.com/mehdi-shokohi/cppm/internal/manifest"
	"github.com/mehdi-shokohi/cppm/internal/project"
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
		Aliases: []string{"apply", "regen"},
		Summary: "apply cppm.json to the CMake files",
		Usage:   "cppm sync [flags]",
		Long: `Applies the manifest to the generated CMake, then reports what changed.

Run this after editing cppm.json by hand. Changing "cxx".standard to 26 and
running sync propagates it to the build; so does changing "toolchain".cxx to
clang++, or adding a dependency option.

Aliases: apply, regen.

What sync owns and may rewrite:
    .cppm/generated/cppm.cmake   language settings and dependencies
    CMakePresets.json            compiler, generator, build types

What sync never touches:
    CMakeLists.txt               yours, including your project() and targets
    CMakeUserPresets.json        yours, for presets of your own

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
			changes, err := Sync(p)
			if err != nil {
				return err
			}
			if *quiet {
				return nil
			}
			if len(changes) == 0 {
				fmt.Printf("already up to date (C++%d, %d dependency(ies))\n",
					p.Manifest.CXX.Standard, len(p.Manifest.Dependencies))
				return nil
			}
			for _, c := range changes {
				fmt.Printf("  %s\n", c)
			}
			fmt.Println("\nrun 'cppm build' to rebuild with these settings")
			return nil
		},
	}
}

// Sync regenerates the cppm-owned CMake files and reports what changed.
//
// Exported because `build` runs it first, so building a freshly cloned project
// works without a separate sync step.
func Sync(p *project.Project) ([]string, error) {
	if err := reconcile(p); err != nil {
		return nil, err
	}
	var changes []string

	// --- the generated include ---
	managed := cmakegen.Managed(p.Manifest, p.Lock, Version)
	previous, _ := os.ReadFile(p.ManagedCMakePath())
	same, err := fileHasContent(p.ManagedCMakePath(), managed)
	if err != nil {
		return nil, err
	}
	if !same {
		if err := atomicfile.WriteString(p.ManagedCMakePath(), managed); err != nil {
			return nil, err
		}
		changes = append(changes, describeManagedChange(string(previous), managed, p.Manifest))
	}

	// --- presets ---
	presets, err := cmakegen.Presets(p.Manifest)
	if err != nil {
		return nil, err
	}
	existing, readErr := os.ReadFile(p.PresetsPath())
	switch {
	case os.IsNotExist(readErr):
		if err := atomicfile.WriteString(p.PresetsPath(), presets); err != nil {
			return nil, err
		}
		changes = append(changes, "created CMakePresets.json")
	case readErr != nil:
		return nil, readErr
	case string(existing) == presets:
		// up to date
	case !cmakegen.PresetsAreCppmGenerated(existing):
		// Somebody wrote this by hand. Overwriting it silently would lose their
		// work, so report instead and let them decide.
		changes = append(changes,
			"skipped CMakePresets.json: no cppm vendor stamp, so cppm did not generate it.\n"+
				"           delete it and run sync again to regenerate, or keep it and set the compiler there yourself")
	default:
		if err := atomicfile.WriteString(p.PresetsPath(), presets); err != nil {
			return nil, err
		}
		changes = append(changes, describePresetChange(p.Manifest))
	}
	return changes, nil
}

// describeManagedChange names the user-visible effect of a regeneration rather
// than just reporting that bytes moved.
func describeManagedChange(before, after string, m *manifest.Manifest) string {
	oldStd := cmakeIntSetting(before, "CMAKE_CXX_STANDARD")
	if before != "" && oldStd != 0 && oldStd != m.CXX.Standard {
		return fmt.Sprintf("C++ standard %d -> %d", oldStd, m.CXX.Standard)
	}
	if before == "" {
		return fmt.Sprintf("generated %s", cmakegen.GeneratedRelPath)
	}
	return fmt.Sprintf("regenerated %s (C++%d, %d dependency(ies))",
		cmakegen.GeneratedRelPath, m.CXX.Standard, len(m.Dependencies))
}

func describePresetChange(m *manifest.Manifest) string {
	parts := []string{"updated CMakePresets.json"}
	if m.Toolchain.CXX != "" {
		parts = append(parts, "compiler "+m.Toolchain.CXX)
	}
	if m.Toolchain.Generator != "" {
		parts = append(parts, "generator "+m.Toolchain.Generator)
	}
	return strings.Join(parts, " -- ")
}

// cmakeIntSetting extracts set(<NAME> <int>) from generated CMake, for change
// reporting only.
func cmakeIntSetting(content, name string) int {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		prefix := "set(" + name + " "
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		v := strings.TrimSuffix(strings.TrimPrefix(line, prefix), ")")
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	}
	return 0
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
					Dev:          dep.Dev,
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
		// dev is intent, not resolution, so the manifest is authoritative: adding
		// "dev": true by hand and running sync must take effect without a
		// re-resolve. This moves no pin.
		locked.Dev = dep.Dev
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
