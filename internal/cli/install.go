package cli

import (
	"flag"
	"fmt"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/lockfile"
	"github.com/mehdi-shokohi/cppm/internal/manifest"
	"github.com/mehdi-shokohi/cppm/internal/project"
	"github.com/mehdi-shokohi/cppm/internal/toolchain"
	"github.com/mehdi-shokohi/cppm/internal/vcs"
)

type installOpts struct {
	noConfigure   bool
	release       bool
	noInteractive bool
	noDiscover    bool
	noSubmodules  bool
}

func cmdInstall() *Command {
	build := func() (*flag.FlagSet, *installOpts) {
		o := &installOpts{}
		fs := newFlagSet("install")
		fs.BoolVar(&o.noConfigure, "no-configure", false, "write the CMake files but do not download anything")
		fs.BoolVar(&o.release, "release", false, "configure the release preset instead of debug")
		fs.BoolVar(&o.noInteractive, "non-interactive", false, "never prompt; record unresolved targets instead")
		fs.BoolVar(&o.noDiscover, "no-discover", false, "do not scan a new dependency's CMake for target names")
		fs.BoolVar(&o.noSubmodules, "no-submodules", false, "do not fetch a new dependency's git submodules while inspecting it")
		return fs, o
	}
	return &Command{
		Name:    "install",
		Aliases: []string{"fetch", "bootstrap"},
		Summary: "set up a cloned project from cppm.json",
		Usage:   "cppm install [flags]",
		Long: `Takes a freshly cloned cppm project from cppm.json to a configured build.

    git clone https://example.com/team/app
    cd app
    cppm install

This is the one command to run after cloning. It needs nothing but cppm.json:

    1. resolves any dependency that cppm.lock does not already pin
    2. regenerates .cppm/generated/cppm.cmake and CMakePresets.json
    3. configures the build tree, which is what downloads the dependencies

install never moves a pin. A dependency already resolved in cppm.lock is
fetched at exactly that commit, which is the whole point of committing the
lockfile -- so a clone of this repository builds the same code a year from now.
Only 'cppm update' re-resolves. If cppm.lock is absent or incomplete, the gaps
are resolved and written, and the result is worth committing.

Dependencies are downloaded by CMake, at step 3, into the build tree. So
--no-configure writes the CMake files and stops before the network work, which
is what to use when you only want the generated files refreshed.

install does not compile anything: run 'cppm build' for that. 'cppm build'
would also do steps 2 and 3 on its own -- install exists because step 1 is not
something build will do for you, and because "what do I run after cloning" has
one obvious answer in every other ecosystem.

This is not 'cmake --install': cppm does not copy build output into a prefix.`,
		flagSet: func() *flag.FlagSet { fs, _ := build(); return fs },
		Run: func(args []string) error {
			fs, o := build()
			if err := parseArgs(fs, args); err != nil {
				return err
			}
			if fs.NArg() > 0 {
				return fmt.Errorf("unexpected argument %q", fs.Arg(0))
			}
			return runInstall(o)
		},
	}
}

func runInstall(o *installOpts) error {
	p, err := project.Open(mustCwd())
	if err != nil {
		return err
	}

	// CMake is needed for step 3 only, but a missing compiler toolchain is
	// worth reporting before the network work rather than after it.
	cmake := toolchain.CMake()
	if !o.noConfigure && !cmake.Found() {
		return fmt.Errorf("cmake not found in PATH; run 'cppm doctor' for details,\n" +
			"or 'cppm install --no-configure' to write the CMake files anyway")
	}

	fmt.Printf("%s: %d dependency(ies) in cppm.json\n", p.Manifest.Name, len(p.Manifest.Dependencies))

	resolved, err := resolveMissingPins(p, o)
	if err != nil {
		return err
	}

	changes, err := Sync(p)
	if err != nil {
		return err
	}
	for _, c := range changes {
		fmt.Printf("  %s\n", c)
	}

	if o.noConfigure {
		fmt.Printf("\n--no-configure: nothing was downloaded. run 'cppm install' or 'cppm build' to fetch.\n")
		return nil
	}

	preset := "debug"
	if o.release {
		preset = "release"
	}
	// configure() syncs again, which is cheap and idempotent, and keeps the
	// single code path that every command driving CMake goes through.
	if err := configure(p, cmake, preset, false, SyncOptions{}); err != nil {
		return err
	}

	fmt.Printf("\n%s is ready. run 'cppm build' to compile it.\n", p.Manifest.Name)
	if len(resolved) > 0 {
		fmt.Printf("\ncppm.lock gained %d pin(s): %s\n", len(resolved), strings.Join(resolved, ", "))
		fmt.Printf("commit it, so everyone else builds these exact commits.\n")
	}
	return nil
}

// resolveMissingPins pins the dependencies that cppm.json declares and
// cppm.lock does not.
//
// Only the missing ones. An existing pin is left exactly as it is, including
// when the manifest's ref has since changed to something else: honouring the
// lockfile is what makes a clone reproducible, and re-resolving here would make
// `install` quietly destroy that. The manifest-vs-lock disagreement is real and
// `cppm doctor` reports it; moving the pin is `cppm update`'s job alone.
func resolveMissingPins(p *project.Project, o *installOpts) ([]string, error) {
	var missing []string
	for _, name := range p.Manifest.DependencyNames() {
		dep := p.Manifest.Dependencies[name]
		if dep.Source != manifest.SourceGit {
			continue // path dependencies need no resolution; reconcile() locks them
		}
		if _, ok := p.Lock.Find(name); !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}

	fmt.Printf("\n%d dependency(ies) not in cppm.lock; resolving:\n", len(missing))
	git := newGitClient()

	for _, name := range missing {
		dep := p.Manifest.Dependencies[name]
		s := vcs.Spec{Name: name, Source: manifest.SourceGit, URL: dep.URL, Ref: dep.Version}

		res, err := resolveInstallRef(git, dep, name)
		if err != nil {
			return nil, err
		}

		// Same tiered discovery `cppm add` uses, so a dependency resolved here
		// gets the same target names it would have had when first added.
		ao := &addOpts{
			targets:       strings.Join(dep.Targets, ","),
			dev:           dep.Dev,
			noInteractive: o.noInteractive,
			noDiscover:    o.noDiscover,
			noSubmodules:  o.noSubmodules,
		}
		targets, origin, options, notes := resolveTargets(git, s, res, ao)
		if len(dep.Options) > 0 {
			options = dep.Options
		}

		p.Lock.Put(lockfile.Package{
			Name:         name,
			Source:       manifest.SourceGit,
			URL:          dep.URL,
			Requested:    dep.Version,
			ResolvedRef:  res.Ref,
			Commit:       res.Commit,
			Targets:      targets,
			TargetOrigin: origin,
			Options:      options,
			Dev:          dep.Dev,
		})
		fmt.Printf("  %s  %s  (%s)\n", name, res.Commit[:12], describeRef(res))
		reportTargets(name, targets, origin)
		if notes != "" {
			fmt.Printf("    note: %s\n", notes)
		}
	}

	if err := p.SaveLock(); err != nil {
		return nil, err
	}
	return missing, nil
}

// resolveInstallRef turns a manifest ref into a commit.
//
// An empty "version" means the manifest never pinned one. install takes the
// default branch rather than prompting: the command exists to be run
// unattended in a fresh clone and in CI, where a prompt is a hang.
func resolveInstallRef(git *vcs.Client, dep manifest.Dependency, name string) (vcs.Resolution, error) {
	if dep.Version == "" {
		res, err := git.DefaultBranch(dep.URL)
		if err != nil {
			return res, fmt.Errorf("%s: %w", name, err)
		}
		fmt.Printf("  %s has no \"version\" in cppm.json; taking the default branch\n", name)
		return res, nil
	}
	res, err := git.Resolve(dep.URL, dep.Version)
	if err != nil {
		return res, fmt.Errorf("%s: %w\ncppm.json asks for %q; fix it there, or run 'cppm update %s'",
			name, err, dep.Version, name)
	}
	return res, nil
}
