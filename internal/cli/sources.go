package cli

import (
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/cmakegen"
	"github.com/mehdi-shokohi/cppm/internal/project"
	"github.com/mehdi-shokohi/cppm/internal/sources"
)

// expandSourceGlobs expands every target's "sourceGlobs" against the project
// root.
//
// The per-pattern results are returned alongside the merged map because the
// `sources` command reports a pattern that matched nothing, and a merged list
// cannot distinguish "no patterns" from "a pattern with a typo in it".
func expandSourceGlobs(p *project.Project) (cmakegen.GlobbedSources, map[string][]sources.Result, error) {
	var merged cmakegen.GlobbedSources
	var results map[string][]sources.Result

	for _, name := range p.Manifest.TargetNames() {
		t := p.Manifest.Targets[name]
		if len(t.SourceGlobs) == 0 {
			continue
		}
		rs, err := sources.Expand(p.Root, t.SourceGlobs)
		if err != nil {
			return nil, nil, fmt.Errorf("target %q: %w", name, err)
		}
		if results == nil {
			results = map[string][]sources.Result{}
		}
		results[name] = rs

		files := sources.Files(rs, t.Sources)
		if len(files) == 0 {
			// Deliberately absent rather than empty: see cmakegen.GlobbedSources.
			continue
		}
		if merged == nil {
			merged = cmakegen.GlobbedSources{}
		}
		merged[name] = files
	}
	return merged, results, nil
}

// globbedSourceCount counts the files the generated CMake attaches through
// "sourceGlobs", by counting the lines writeGlobbedSources emits for them.
//
// Reading it back out of the rendered text rather than recomputing keeps sync's
// report honest: it describes the file that was actually replaced.
func globbedSourceCount(generated string) int {
	return strings.Count(generated, `"${CMAKE_CURRENT_SOURCE_DIR}/`)
}

type sourcesOpts struct {
	target string
}

func cmdSources() *Command {
	build := func() (*flag.FlagSet, *sourcesOpts) {
		o := &sourcesOpts{}
		fs := newFlagSet("sources")
		fs.StringVar(&o.target, "target", "", "the target the patterns belong to (default: the only target)")
		return fs, o
	}
	return &Command{
		Name:    "sources",
		Aliases: []string{"src"},
		Summary: "add a folder or wildcard of source files to a target",
		Usage:   "cppm sources [add|remove] <pattern>... [flags]",
		Long: `Records source patterns in cppm.json and compiles the files they match.

    cppm sources add src/net              every C++ source under src/net
    cppm sources add "src/**/*.cpp"       the same, for the whole tree
    cppm sources add "src/codec/*.cpp"    one directory, not its subdirectories
    cppm sources                          list the patterns and what they match
    cppm sources remove src/net           stop compiling them

Quote a pattern containing * so the shell does not expand it first.

Pattern syntax is CMake's and the shell's: '*' and '?' and [class] match within
one path segment, and '**' spans directories. A pattern naming a directory
means every C++ source beneath it (` + strings.Join(sources.Extensions, ", ") + `), which is
the form to prefer: a file added to that directory later needs no new pattern.

How this reaches the build, and the one thing to know about it:

cppm does not edit CMakeLists.txt -- that is your file and the promise is
absolute. So the matched files are written into .cppm/generated/cppm.cmake and
attached to your target there. The expansion happens at 'cppm sync' time rather
than in CMake, which keeps the file list reviewable in a diff, and means a
newly created source file is compiled only after a sync. 'cppm build' syncs
first, so in practice building picks it up.

Files already listed in "sources" in cppm.json are skipped, since your own
CMakeLists.txt names those.

Directories cppm never searches: anything beginning with a dot, build/, and any
configured CMake build tree. Without that, "**/*.cpp" would compile every
dependency's source into your target.`,
		flagSet: func() *flag.FlagSet { fs, _ := build(); return fs },
		Run: func(args []string) error {
			fs, o := build()
			if err := parseArgs(fs, args); err != nil {
				return err
			}
			rest := fs.Args()
			verb := "list"
			if len(rest) > 0 {
				switch rest[0] {
				case "add", "remove", "rm", "list":
					verb, rest = rest[0], rest[1:]
				default:
					return fmt.Errorf("expected 'add', 'remove' or no verb, got %q\n"+
						"to add a pattern: cppm sources add %s", rest[0], rest[0])
				}
			}

			p, err := project.Open(mustCwd())
			if err != nil {
				return err
			}
			switch verb {
			case "list":
				if len(rest) > 0 {
					return fmt.Errorf("unexpected argument %q; did you mean 'cppm sources add %s'?", rest[0], rest[0])
				}
				return runSourcesList(p, o)
			case "add":
				return runSourcesEdit(p, o, rest, true)
			default:
				return runSourcesEdit(p, o, rest, false)
			}
		},
	}
}

// resolveTarget picks the target the patterns apply to.
//
// With one target the answer is unambiguous. With several, cppm declines rather
// than picking: compiling a file into the wrong target produces a build that
// succeeds and is wrong, which is the failure mode this project avoids
// everywhere else too.
func resolveTarget(p *project.Project, want string) (string, error) {
	names := p.Manifest.TargetNames()
	if want != "" {
		if _, ok := p.Manifest.Targets[want]; !ok {
			return "", fmt.Errorf("no target %q in cppm.json; it declares: %s", want, strings.Join(names, ", "))
		}
		return want, nil
	}
	switch len(names) {
	case 0:
		return "", fmt.Errorf("cppm.json declares no targets")
	case 1:
		return names[0], nil
	}
	return "", fmt.Errorf("this project has %d targets, so --target is required:\n    %s",
		len(names), strings.Join(names, "\n    "))
}

func runSourcesList(p *project.Project, o *sourcesOpts) error {
	_, results, err := expandSourceGlobs(p)
	if err != nil {
		return err
	}
	if o.target != "" {
		name, err := resolveTarget(p, o.target)
		if err != nil {
			return err
		}
		results = map[string][]sources.Result{name: results[name]}
	}
	if len(results) == 0 {
		fmt.Printf("no source patterns in cppm.json\n\n")
		fmt.Printf("add one with, for example:\n    cppm sources add src\n")
		return nil
	}

	names := make([]string, 0, len(results))
	for n := range results {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		t := p.Manifest.Targets[name]
		fmt.Printf("%s\n", name)
		for _, r := range results[name] {
			what := r.Pattern
			if r.Dir {
				what = r.Pattern + "/  (directory)"
			}
			fmt.Printf("  %s\n", what)
			if len(r.Files) == 0 {
				fmt.Printf("      nothing matched yet\n")
				continue
			}
			for _, f := range r.Files {
				note := ""
				if contains(t.Sources, f) {
					note = "   (already in \"sources\"; skipped)"
				}
				fmt.Printf("      %s%s\n", f, note)
			}
		}
		// "added to", not "compiled": an explicit pattern may name headers, and
		// CMake infers HEADER_FILE_ONLY from the extension rather than
		// compiling them.
		fmt.Printf("  %d file(s) added to %s by these patterns\n\n",
			len(sources.Files(results[name], t.Sources)), name)
	}
	return nil
}

func runSourcesEdit(p *project.Project, o *sourcesOpts, patterns []string, add bool) error {
	if len(patterns) == 0 {
		verb := "remove"
		if add {
			verb = "add"
		}
		return fmt.Errorf("expected at least one pattern, e.g. cppm sources %s src/net", verb)
	}
	name, err := resolveTarget(p, o.target)
	if err != nil {
		return err
	}
	t := p.Manifest.Targets[name]

	// Trailing slashes are how people type a directory and mean nothing to the
	// matcher, so normalise before comparing or storing: "src/net/" and
	// "src/net" must not both end up in cppm.json.
	clean := make([]string, 0, len(patterns))
	for _, raw := range patterns {
		pat := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(raw), "./"), "/")
		if err := sources.Validate(pat); err != nil {
			return err
		}
		clean = append(clean, pat)
	}

	var added, already, removed, absent []string
	for _, pat := range clean {
		switch {
		case add && contains(t.SourceGlobs, pat):
			already = append(already, pat)
		case add:
			t.SourceGlobs = append(t.SourceGlobs, pat)
			added = append(added, pat)
		case contains(t.SourceGlobs, pat):
			t.SourceGlobs = remove(t.SourceGlobs, pat)
			removed = append(removed, pat)
		default:
			absent = append(absent, pat)
		}
	}
	if len(absent) > 0 {
		return fmt.Errorf("target %q has no such pattern: %s (see 'cppm sources')",
			name, strings.Join(absent, ", "))
	}
	if len(t.Sources) == 0 && len(t.SourceGlobs) == 0 {
		return fmt.Errorf("removing %s would leave target %q with no sources at all;\n"+
			"a target with nothing to compile cannot be configured", strings.Join(clean, ", "), name)
	}

	p.Manifest.Targets[name] = t
	if err := p.SaveManifest(); err != nil {
		return err
	}

	// Expand after saving so the report describes the state on disk, and run a
	// full sync so the generated CMake is correct the moment this returns.
	_, results, err := expandSourceGlobs(p)
	if err != nil {
		return err
	}
	if _, err := Sync(p); err != nil {
		return err
	}

	for _, pat := range added {
		fmt.Printf("  added    %s -> %d file(s)\n", pat, matchedCount(results[name], pat))
	}
	for _, pat := range removed {
		fmt.Printf("  removed  %s\n", pat)
	}
	for _, pat := range already {
		fmt.Printf("  kept     %s (already recorded)\n", pat)
	}

	total := len(sources.Files(results[name], t.Sources))
	fmt.Printf("\n%s now takes %d file(s) from %d pattern(s), plus %d listed in CMakeLists.txt\n",
		name, total, len(t.SourceGlobs), len(t.Sources))

	for _, pat := range added {
		if matchedCount(results[name], pat) == 0 {
			fmt.Printf("\nwarning: %q matched no files yet. That is fine if you are about to\n", pat)
			fmt.Printf("         create them -- run 'cppm sync' afterwards -- but check the\n")
			fmt.Printf("         spelling if you expected matches now.\n")
		}
	}
	if len(added) > 0 {
		fmt.Printf("\nrun 'cppm build' to compile them\n")
	}
	return nil
}

func matchedCount(results []sources.Result, pattern string) int {
	for _, r := range results {
		if r.Pattern == pattern {
			return len(r.Files)
		}
	}
	return 0
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func remove(list []string, drop string) []string {
	var out []string
	for _, s := range list {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}
