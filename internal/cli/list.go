package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"cppm/internal/lockfile"
	"cppm/internal/manifest"
	"cppm/internal/project"
)

func cmdList() *Command {
	build := func() (*flag.FlagSet, *bool) {
		long := false
		fs := newFlagSet("list")
		fs.BoolVar(&long, "long", false, "also show resolved commits and target origin")
		return fs, &long
	}
	return &Command{
		Name:    "list",
		Summary: "list the project's dependencies",
		Usage:   "cppm list [flags]",
		flagSet: func() *flag.FlagSet { fs, _ := build(); return fs },
		Run: func(args []string) error {
			fs, long := build()
			if err := parseArgs(fs, args); err != nil {
				return err
			}
			p, err := project.Open(mustCwd())
			if err != nil {
				return err
			}
			names := p.Manifest.DependencyNames()
			if len(names) == 0 {
				fmt.Printf("%s has no dependencies.\n\nadd one:\n    cppm add github:fmtlib/fmt\n", p.Manifest.Name)
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			if *long {
				fmt.Fprintln(w, "NAME\tSOURCE\tREQUESTED\tCOMMIT\tTARGETS\tORIGIN")
			} else {
				fmt.Fprintln(w, "NAME\tSOURCE\tREQUESTED\tTARGETS")
			}
			for _, n := range names {
				d := p.Manifest.Dependencies[n]
				origin, commit, targets := "-", "-", "-"
				if lp, ok := p.Lock.Find(n); ok {
					if lp.TargetOrigin != "" {
						origin = lp.TargetOrigin
					}
					if lp.Commit != "" {
						commit = lp.Commit[:min(7, len(lp.Commit))]
					}
					if len(lp.Targets) > 0 {
						targets = strings.Join(lp.Targets, ",")
					}
				}
				requested := d.Version
				if d.Source == manifest.SourcePath {
					requested = d.Path
				}
				if requested == "" {
					requested = "(default branch)"
				}
				if *long {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", n, d.Source, requested, commit, targets, origin)
				} else {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", n, d.Source, requested, targets)
				}
			}
			if err := w.Flush(); err != nil {
				return err
			}

			// Guesses are surfaced rather than hidden: an inferred target name
			// is the most likely thing to be wrong in a working project.
			var guessed []string
			for _, p := range p.Lock.Packages {
				switch p.TargetOrigin {
				case lockfile.OriginInferred, lockfile.OriginProbed:
					guessed = append(guessed, p.Name)
				}
			}
			if len(guessed) > 0 {
				fmt.Printf("\nnote: target names for %s were inferred, not declared.\n", strings.Join(guessed, ", "))
				fmt.Println("      if linking fails, pin them with \"targets\" in cppm.json")
			}
			return nil
		},
	}
}

func cmdInfo() *Command {
	return &Command{
		Name:    "info",
		Summary: "show everything known about one dependency",
		Usage:   "cppm info <dependency>",
		Run: func(args []string) error {
			fs := newFlagSet("info")
			if err := parseArgs(fs, args); err != nil {
				return err
			}
			if fs.NArg() != 1 {
				return fmt.Errorf("expected exactly one dependency name")
			}
			name := fs.Arg(0)

			p, err := project.Open(mustCwd())
			if err != nil {
				return err
			}
			d, ok := p.Manifest.Dependencies[name]
			if !ok {
				return fmt.Errorf("no dependency named %q in %s (see 'cppm list')", name, manifest.FileName)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "name\t%s\n", name)
			fmt.Fprintf(w, "source\t%s\n", d.Source)
			if d.URL != "" {
				fmt.Fprintf(w, "url\t%s\n", d.URL)
			}
			if d.Path != "" {
				fmt.Fprintf(w, "path\t%s\n", d.Path)
			}
			if d.Version != "" {
				fmt.Fprintf(w, "requested\t%s\n", d.Version)
			}
			if lp, ok := p.Lock.Find(name); ok {
				if lp.ResolvedRef != "" {
					fmt.Fprintf(w, "resolved ref\t%s\n", lp.ResolvedRef)
				}
				if lp.Commit != "" {
					fmt.Fprintf(w, "commit\t%s\n", lp.Commit)
				}
				if len(lp.Targets) > 0 {
					fmt.Fprintf(w, "targets\t%s\n", strings.Join(lp.Targets, " "))
				}
				if lp.TargetOrigin != "" {
					fmt.Fprintf(w, "target origin\t%s\n", lp.TargetOrigin)
				}
			} else {
				fmt.Fprintf(w, "lock\tnot resolved yet (run 'cppm sync')\n")
			}
			for _, k := range sortedMapKeys(d.Options) {
				fmt.Fprintf(w, "option %s\t%s\n", k, d.Options[k])
			}
			return w.Flush()
		},
	}
}

func sortedMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
