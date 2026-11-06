package cli

import (
	"flag"
	"fmt"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/manifest"
	"github.com/mehdi-shokohi/cppm/internal/project"
	"github.com/mehdi-shokohi/cppm/internal/vcs"
)

// Resolution is vcs.Resolution, aliased for brevity in this file.
type Resolution = vcs.Resolution

// newGitClient returns the git client the commands share.
func newGitClient() *vcs.Client { return &vcs.Client{} }

func cmdRemove() *Command {
	return &Command{
		Name:    "remove",
		Aliases: []string{"rm"},
		Summary: "remove a dependency",
		Usage:   "cppm remove <dependency>...",
		Long: `Removes dependencies from cppm.json and cppm.lock, then regenerates the
managed CMake.

Any target that lists the dependency is updated too. Nothing in your
CMakeLists.txt is touched: if you linked the dependency by hand there, cppm
leaves that for you to remove.`,
		Run: func(args []string) error {
			fs := newFlagSet("remove")
			if err := parseArgs(fs, args); err != nil {
				return err
			}
			if fs.NArg() == 0 {
				return fmt.Errorf("expected at least one dependency name (see 'cppm list')")
			}
			p, err := project.Open(mustCwd())
			if err != nil {
				return err
			}

			var removed, missing []string
			for _, name := range fs.Args() {
				if _, ok := p.Manifest.Dependencies[name]; !ok {
					missing = append(missing, name)
					continue
				}
				delete(p.Manifest.Dependencies, name)
				p.Lock.Remove(name)

				// A target listing the removed dependency would fail validation.
				for tn, t := range p.Manifest.Targets {
					var kept []string
					for _, d := range t.Dependencies {
						if d != name {
							kept = append(kept, d)
						}
					}
					if len(kept) != len(t.Dependencies) {
						t.Dependencies = kept
						p.Manifest.Targets[tn] = t
					}
				}
				removed = append(removed, name)
			}

			if len(missing) > 0 {
				return fmt.Errorf("not a dependency: %s (see 'cppm list')", strings.Join(missing, ", "))
			}
			if err := p.SaveManifest(); err != nil {
				return err
			}
			if _, err := Sync(p); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", strings.Join(removed, ", "))
			if n := len(p.Manifest.Dependencies); n > 0 {
				fmt.Printf("%d dependency(ies) remain\n", n)
			}
			fmt.Printf("\nnote: cppm does not edit CMakeLists.txt, so remove any\n")
			fmt.Printf("      target_link_libraries() lines you added by hand.\n")
			return nil
		},
	}
}

func cmdUpdate() *Command {
	build := func() (*flag.FlagSet, *bool) {
		dry := false
		fs := newFlagSet("update")
		fs.BoolVar(&dry, "dry-run", false, "report what would change without writing")
		return fs, &dry
	}
	return &Command{
		Name:    "update",
		Summary: "re-resolve dependency refs to their current commits",
		Usage:   "cppm update [<dependency>...] [flags]",
		Long: `Re-resolves each dependency's ref and updates cppm.lock.

This is the only command that moves a pin. It does NOT look for newer releases:
cppm never compares version strings, so "newer" is not something it can
determine. What it does is re-resolve the ref you asked for --

    a branch   moves to that branch's current commit
    a tag      normally stays put, but a tag that was re-pointed is reported
    a commit   never moves

A moved tag is worth knowing about: it means a published release changed
underneath you.`,
		flagSet: func() *flag.FlagSet { fs, _ := build(); return fs },
		Run: func(args []string) error {
			fs, dry := build()
			if err := parseArgs(fs, args); err != nil {
				return err
			}
			p, err := project.Open(mustCwd())
			if err != nil {
				return err
			}

			wanted := fs.Args()
			if len(wanted) == 0 {
				wanted = p.Manifest.DependencyNames()
			}
			if len(wanted) == 0 {
				fmt.Println("no dependencies to update")
				return nil
			}
			return runUpdate(p, wanted, *dry)
		},
	}
}

func runUpdate(p *project.Project, names []string, dry bool) error {
	git := newGitClient()
	var changed, unchanged, retagged []string

	for _, name := range names {
		dep, ok := p.Manifest.Dependencies[name]
		if !ok {
			return fmt.Errorf("not a dependency: %s", name)
		}
		if dep.Source != manifest.SourceGit {
			continue // path dependencies are editable; there is nothing to pin
		}
		ref := dep.Version
		if ref == "" {
			res, err := git.DefaultBranch(dep.URL)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if err := applyUpdate(p, name, res, dry, &changed, &unchanged, &retagged); err != nil {
				return err
			}
			continue
		}
		res, err := git.Resolve(dep.URL, ref)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := applyUpdate(p, name, res, dry, &changed, &unchanged, &retagged); err != nil {
			return err
		}
	}

	for _, s := range changed {
		fmt.Printf("  updated   %s\n", s)
	}
	for _, s := range retagged {
		fmt.Printf("  RETAGGED  %s\n", s)
	}
	if len(changed) == 0 && len(retagged) == 0 {
		fmt.Printf("all %d dependency(ies) already at their resolved commits\n", len(unchanged))
		return nil
	}
	if dry {
		fmt.Println("\n--dry-run: nothing was written")
		return nil
	}
	if err := p.SaveLock(); err != nil {
		return err
	}
	if _, err := Sync(p); err != nil {
		return err
	}
	if len(retagged) > 0 {
		fmt.Printf("\nwarning: a tag now points at a different commit than before.\n")
		fmt.Printf("         the published release changed; review before shipping.\n")
	}
	fmt.Println("\nrun 'cppm build' to rebuild")
	return nil
}

func applyUpdate(p *project.Project, name string, res Resolution, dry bool, changed, unchanged, retagged *[]string) error {
	locked, ok := p.Lock.Find(name)
	if ok && locked.Commit == res.Commit {
		*unchanged = append(*unchanged, name)
		return nil
	}
	old := "(unpinned)"
	if ok && locked.Commit != "" {
		old = locked.Commit[:12]
	}
	entry := fmt.Sprintf("%s  %s -> %s  (%s)", name, old, res.Commit[:12], describeRef(res))

	// A tag resolving to a new commit means the release itself was changed,
	// which is different from a branch simply advancing.
	isTag := strings.HasPrefix(res.Ref, "refs/tags/")
	if isTag && ok && locked.Commit != "" {
		*retagged = append(*retagged, entry)
	} else {
		*changed = append(*changed, entry)
	}

	if dry {
		return nil
	}
	if ok {
		locked.ResolvedRef = res.Ref
		locked.Commit = res.Commit
	}
	return nil
}
