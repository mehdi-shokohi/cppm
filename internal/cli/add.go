package cli

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/discover"
	"github.com/mehdi-shokohi/cppm/internal/lockfile"
	"github.com/mehdi-shokohi/cppm/internal/manifest"
	"github.com/mehdi-shokohi/cppm/internal/project"
	"github.com/mehdi-shokohi/cppm/internal/recipes"
	"github.com/mehdi-shokohi/cppm/internal/vcs"
)

type addOpts struct {
	name          string
	targets       string
	noInteractive bool
	noDiscover    bool
	forceReplace  bool
}

func cmdAdd() *Command {
	build := func() (*flag.FlagSet, *addOpts) {
		o := &addOpts{}
		fs := newFlagSet("add")
		fs.StringVar(&o.name, "name", "", "dependency name (default: derived from the repository)")
		fs.StringVar(&o.targets, "target", "", "CMake target(s) to link, comma-separated; skips discovery")
		fs.BoolVar(&o.noInteractive, "non-interactive", false, "never prompt; record unresolved targets instead")
		fs.BoolVar(&o.noDiscover, "no-discover", false, "do not download the source to look for targets")
		fs.BoolVar(&o.forceReplace, "force", false, "replace an existing dependency of the same name")
		return fs, o
	}
	return &Command{
		Name:    "add",
		Summary: "add a dependency",
		Usage:   "cppm add <spec> [flags]",
		Long: `Resolves a dependency, pins it to a commit, and wires it into CMake.

    cppm add github:fmtlib/fmt@11.1.4
    cppm add github:nlohmann/json@v3.11.3
    cppm add gitlab:libeigen/eigen@3.4.0
    cppm add https://example.com/team/lib.git@main
    cppm add local:../MyLibrary

A version is an opaque git ref: a tag, branch or commit. cppm does not parse or
compare versions, so a library that ignores semver is not a problem. Ranges such
as ^1.2 are deliberately unsupported.

With no @ref, cppm lists the repository's tags and asks which to pin. Pass
--non-interactive to take the default branch instead.

Target names are found in this order: --target, then the curated recipe
database, then a scan of the dependency's own CMake. If none of those is
confident, cppm asks rather than guessing, because a wrong target name produces
a build that links the wrong thing.`,
		flagSet: func() *flag.FlagSet { fs, _ := build(); return fs },
		Run: func(args []string) error {
			fs, o := build()
			if err := parseArgs(fs, args); err != nil {
				return err
			}
			if fs.NArg() != 1 {
				return fmt.Errorf("expected exactly one dependency specification, e.g. github:fmtlib/fmt@11.1.4")
			}
			return runAdd(fs.Arg(0), o)
		},
	}
}

func runAdd(spec string, o *addOpts) error {
	p, err := project.Open(mustCwd())
	if err != nil {
		return err
	}

	s, err := vcs.ParseSpec(spec)
	if err != nil {
		return err
	}
	if o.name != "" {
		s.Name = o.name
	}
	if !vcs.ValidName(s.Name) {
		return fmt.Errorf("derived dependency name %q is not usable; pass --name", s.Name)
	}
	if _, exists := p.Manifest.Dependencies[s.Name]; exists && !o.forceReplace {
		return fmt.Errorf("%q is already a dependency; use --force to replace it, or 'cppm remove %s' first", s.Name, s.Name)
	}

	if s.Source == "path" {
		return addPath(p, s)
	}
	return addGit(p, s, o)
}

// addPath records a local dependency. Nothing is resolved or pinned: a path
// dependency is editable by design.
func addPath(p *project.Project, s vcs.Spec) error {
	abs := s.Path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(p.Root, s.Path)
	}
	abs = filepath.Clean(abs)

	if fi, err := os.Stat(abs); err != nil {
		return fmt.Errorf("%s does not exist", abs)
	} else if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", abs)
	}
	if _, err := os.Stat(filepath.Join(abs, "CMakeLists.txt")); err != nil {
		return fmt.Errorf("%s has no CMakeLists.txt, so it cannot be added with add_subdirectory", abs)
	}
	if sameOrInside(abs, p.Root) {
		return fmt.Errorf("%s is inside this project; add its directory with add_subdirectory in CMakeLists.txt instead", abs)
	}

	res, err := discover.Scan(abs, s.Name)
	if err != nil {
		return err
	}
	targets := res.Candidates
	var chosen []string
	origin := lockfile.OriginUnresolved
	if res.Confident && len(targets) > 0 {
		chosen = []string{res.Best}
		origin = lockfile.OriginInferred
	}

	p.Manifest.Dependencies[s.Name] = manifest.Dependency{
		Source:  manifest.SourcePath,
		Path:    filepath.ToSlash(s.Path),
		Targets: chosen,
	}
	p.Lock.Put(lockfile.Package{
		Name:         s.Name,
		Source:       manifest.SourcePath,
		Path:         filepath.ToSlash(s.Path),
		Targets:      chosen,
		TargetOrigin: origin,
	})
	if err := p.SaveManifest(); err != nil {
		return err
	}
	if _, err := Sync(p); err != nil {
		return err
	}
	fmt.Printf("added %s (local %s)\n", s.Name, s.Path)
	reportTargets(s.Name, chosen, origin)
	return nil
}

func addGit(p *project.Project, s vcs.Spec, o *addOpts) error {
	git := &vcs.Client{}

	// --- resolve the ref to an immutable commit ---
	var res vcs.Resolution
	var err error
	if s.Ref == "" {
		res, err = chooseRef(git, s.URL, o.noInteractive)
	} else {
		fmt.Printf("resolving %s@%s\n", s.URL, s.Ref)
		res, err = git.Resolve(s.URL, s.Ref)
	}
	if err != nil {
		return err
	}
	fmt.Printf("  %s -> %s\n", describeRef(res), res.Commit[:12])

	// --- targets and options ---
	targets, origin, options, notes := resolveTargets(git, s, res, o)

	dep := manifest.Dependency{
		Source:  manifest.SourceGit,
		URL:     s.URL,
		Version: res.Requested,
		Options: options,
	}
	// Only an explicit choice is written to the manifest; an inferred name lives
	// in the lock, so re-resolution can improve on it later.
	if origin == lockfile.OriginExplicit || origin == lockfile.OriginUser {
		dep.Targets = targets
	}
	if dep.Version == "" {
		dep.Version = strings.TrimPrefix(res.Ref, "refs/heads/")
	}

	p.Manifest.Dependencies[s.Name] = dep
	p.Lock.Put(lockfile.Package{
		Name:         s.Name,
		Source:       manifest.SourceGit,
		URL:          s.URL,
		Requested:    dep.Version,
		ResolvedRef:  res.Ref,
		Commit:       res.Commit,
		Targets:      targets,
		TargetOrigin: origin,
		Options:      options,
	})

	if err := p.SaveManifest(); err != nil {
		return err
	}
	if _, err := Sync(p); err != nil {
		return err
	}

	fmt.Printf("\nadded %s %s\n", s.Name, dep.Version)
	reportTargets(s.Name, targets, origin)
	if notes != "" {
		fmt.Printf("  note: %s\n", notes)
	}
	fmt.Println("\nrun 'cppm build' to fetch and compile it")
	return nil
}

// resolveTargets applies the discovery tiers in order.
func resolveTargets(git *vcs.Client, s vcs.Spec, res vcs.Resolution, o *addOpts) (targets []string, origin string, options map[string]string, notes string) {
	// Tier 1: explicit.
	if o.targets != "" {
		for _, t := range strings.Split(o.targets, ",") {
			if t = strings.TrimSpace(t); t != "" {
				targets = append(targets, t)
			}
		}
		return targets, lockfile.OriginExplicit, nil, ""
	}

	// Tier 2: curated recipe.
	if r, ok := recipes.Lookup(s.URL); ok {
		if len(r.Targets) > 0 {
			fmt.Printf("  recipe: %s\n", strings.Join(r.Targets, " "))
			return r.Targets, lockfile.OriginRecipe, r.Options, r.Notes
		}
		// A recipe with no targets records that the library exports none.
		return nil, lockfile.OriginUnresolved, r.Options, r.Notes
	}

	if o.noDiscover {
		return nil, lockfile.OriginUnresolved, nil, "discovery skipped; set \"targets\" in cppm.json"
	}

	// Tier 3: scan the source.
	dir := vcs.SourceDir(s.URL, res.Commit)
	fmt.Printf("  fetching source to inspect its CMake\n")
	if _, err := git.FetchSource(s.URL, res.Commit, dir); err != nil {
		fmt.Printf("  warning: could not fetch source (%v); targets left unresolved\n", err)
		return nil, lockfile.OriginUnresolved, nil, ""
	}
	scan, err := discover.Scan(dir, s.Name)
	if err != nil {
		return nil, lockfile.OriginUnresolved, nil, ""
	}

	// Tier 5: ask, when unconfident or genuinely ambiguous.
	if !scan.Confident || scan.Ambiguous() {
		if chosen, ok := askForTarget(s.Name, scan, o.noInteractive); ok {
			return []string{chosen}, lockfile.OriginUser, nil, ""
		}
		why := scan.Why
		if why == "" {
			why = "more than one plausible target"
		}
		return nil, lockfile.OriginUnresolved, nil, why
	}
	return []string{scan.Best}, lockfile.OriginInferred, nil, ""
}

// chooseRef handles `cppm add <repo>` with no ref.
//
// cppm will not pick a version by interpreting version strings, so either the
// user picks from the tag list or the default branch is used.
func chooseRef(git *vcs.Client, repoURL string, nonInteractive bool) (vcs.Resolution, error) {
	tags, err := git.Tags(repoURL)
	if err != nil {
		return vcs.Resolution{}, err
	}
	if len(tags) == 0 || nonInteractive || !isInteractive() {
		res, err := git.DefaultBranch(repoURL)
		if err != nil {
			return res, err
		}
		fmt.Printf("  no ref given; using the default branch %s\n", strings.TrimPrefix(res.Ref, "refs/heads/"))
		if len(tags) > 0 {
			fmt.Printf("  this repository has tags, e.g. %s -- pass @<tag> to pin a release\n", strings.Join(firstN(tags, 3), ", "))
		}
		return res, nil
	}

	fmt.Printf("\n%s has %d tags. Most recent-looking first:\n", repoURL, len(tags))
	shown := firstN(tags, 10)
	for i, t := range shown {
		fmt.Printf("  %2d) %s\n", i+1, t)
	}
	fmt.Printf("   0) use the default branch instead\n")
	fmt.Printf("\ncppm does not compare version numbers, so pick the one you want.\n")
	fmt.Printf("choice [1]: ")

	line := readLine()
	if line == "" {
		line = "1"
	}
	n, err := strconv.Atoi(line)
	switch {
	case err != nil:
		// Treat free text as a ref the user typed.
		return git.Resolve(repoURL, line)
	case n == 0:
		return git.DefaultBranch(repoURL)
	case n >= 1 && n <= len(shown):
		return git.Resolve(repoURL, shown[n-1])
	default:
		return vcs.Resolution{}, fmt.Errorf("choice %d is out of range", n)
	}
}

// askForTarget presents candidates and lets the user choose.
//
// For an ambiguous library this is the correct resolution, not a fallback:
// Catch2::Catch2 versus Catch2::Catch2WithMain is a decision only the user can
// make.
func askForTarget(dep string, scan discover.Result, nonInteractive bool) (string, bool) {
	if nonInteractive || !isInteractive() || len(scan.Candidates) == 0 {
		return "", false
	}
	fmt.Printf("\ncppm is not certain which target %s expects you to link.\n", dep)
	if scan.Why != "" {
		fmt.Printf("  (%s)\n", scan.Why)
	}
	shown := scan.TopNames(5)
	for i, name := range shown {
		fmt.Printf("  %2d) %s\n", i+1, name)
	}
	fmt.Printf("   0) decide later -- cppm will tell you where to put it\n")
	fmt.Printf("choice [1]: ")

	line := readLine()
	if line == "" {
		line = "1"
	}
	n, err := strconv.Atoi(line)
	switch {
	case err != nil && line != "":
		return line, true // a name typed by hand
	case n == 0:
		return "", false
	case n >= 1 && n <= len(shown):
		return shown[n-1], true
	}
	return "", false
}

func reportTargets(dep string, targets []string, origin string) {
	if len(targets) == 0 {
		fmt.Printf("  targets:  unresolved\n")
		fmt.Printf("            cppm could not determine what to link. Set it in cppm.json:\n")
		fmt.Printf("              \"%s\": { ..., \"targets\": [\"<real::target>\"] }\n", dep)
		fmt.Printf("            then run 'cppm sync'. Or link it yourself in CMakeLists.txt.\n")
		return
	}
	fmt.Printf("  targets:  %s (%s)\n", strings.Join(targets, " "), origin)
	if origin == lockfile.OriginInferred {
		fmt.Printf("            inferred, not declared -- if linking fails, pin it in cppm.json\n")
	}
}

func describeRef(res vcs.Resolution) string {
	switch {
	case res.Ref == "":
		return "commit " + res.Requested
	case strings.HasPrefix(res.Ref, "refs/tags/"):
		return "tag " + strings.TrimPrefix(res.Ref, "refs/tags/")
	case strings.HasPrefix(res.Ref, "refs/heads/"):
		return "branch " + strings.TrimPrefix(res.Ref, "refs/heads/")
	}
	return res.Ref
}

// isInteractive reports whether stdin is a terminal.
//
// Prompting in a script or CI job would hang the build forever, so every prompt
// is gated on this.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func readLine() string {
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

func firstN(s []string, n int) []string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

// sameOrInside reports whether child is root or lies beneath it.
func sameOrInside(child, root string) bool {
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}
