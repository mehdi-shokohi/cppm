package vcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultTimeout bounds every network operation. A hung fetch is worse than a
// failed one, because the user cannot tell the difference from a slow clone.
const DefaultTimeout = 5 * time.Minute

// Ref is one entry from git ls-remote.
type Ref struct {
	Hash string
	Name string // full ref name, e.g. "refs/tags/11.1.4"
}

// Client runs git. The zero value is usable.
type Client struct {
	// Git is the git executable; "git" when empty.
	Git string
	// Timeout bounds each operation; DefaultTimeout when zero.
	Timeout time.Duration
	// Verbose streams git's own progress output.
	Verbose bool
}

func (c *Client) bin() string {
	if c.Git != "" {
		return c.Git
	}
	return "git"
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// env returns an environment that never prompts.
//
// Without this, a private or mistyped repository makes git block on a
// credential prompt, and cppm appears to hang.
func (c *Client) env() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GCM_INTERACTIVE=never",
		// Dependency sources are read-only to cppm; LFS payloads are not needed
		// to inspect CMake files and would cost a lot of bandwidth.
		"GIT_LFS_SKIP_SMUDGE=1",
	)
}

// run executes git with the given arguments, returning stdout.
func (c *Client) run(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Dir = dir
	cmd.Env = c.env()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if c.Verbose {
		cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	}
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git %s timed out after %s", args[0], c.timeout())
		}
		return "", fmt.Errorf("git %s: %s", args[0], gitError(stderr.String(), err))
	}
	return string(out), nil
}

// gitError extracts the useful line from git's stderr.
func gitError(stderr string, fallback error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		l = strings.TrimPrefix(l, "fatal: ")
		l = strings.TrimPrefix(l, "error: ")
		if l != "" && !strings.HasPrefix(l, "warning:") {
			return l
		}
	}
	if fallback != nil {
		return fallback.Error()
	}
	return "unknown error"
}

// LsRemote lists the remote's refs without cloning anything.
func (c *Client) LsRemote(repoURL string) ([]Ref, error) {
	if err := validateURL(repoURL); err != nil {
		return nil, err
	}
	// "--" stops git reading a URL that begins with "-" as a flag.
	out, err := c.run("", "ls-remote", "--", repoURL)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		refs = append(refs, Ref{Hash: fields[0], Name: fields[1]})
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("%s: no refs found; is it a git repository?", repoURL)
	}
	return refs, nil
}

// Resolution is the outcome of resolving a requested ref.
type Resolution struct {
	// Ref is the full ref that matched, e.g. "refs/tags/11.1.4". Empty when the
	// request was a raw commit hash.
	Ref string
	// Commit is the full 40-character object name.
	Commit string
	// Requested echoes what was asked for.
	Requested string
}

// Resolve turns a requested ref into an immutable commit.
//
// cppm does not interpret versions, so this is pure lookup: the ref is tried as
// a tag, then a branch, then a raw commit. An annotated tag is dereferenced to
// the commit it points at, which is what "GIT_TAG <hash>" needs.
func (c *Client) Resolve(repoURL, ref string) (Resolution, error) {
	if err := validateRef(ref); err != nil {
		return Resolution{}, err
	}
	refs, err := c.LsRemote(repoURL)
	if err != nil {
		return Resolution{}, err
	}
	return resolveFrom(refs, ref)
}

// resolveFrom is the pure part of Resolve, so it can be tested without network.
func resolveFrom(refs []Ref, ref string) (Resolution, error) {
	byName := make(map[string]string, len(refs))
	for _, r := range refs {
		byName[r.Name] = r.Hash
	}

	// An annotated tag has both refs/tags/X (the tag object) and refs/tags/X^{}
	// (the commit). The commit is what we want to pin.
	candidates := []string{
		"refs/tags/" + ref + "^{}",
		"refs/tags/" + ref,
		"refs/heads/" + ref,
		"refs/remotes/origin/" + ref,
		ref,
	}
	for _, name := range candidates {
		if hash, ok := byName[name]; ok {
			resolved := strings.TrimSuffix(name, "^{}")
			return Resolution{Ref: resolved, Commit: hash, Requested: ref}, nil
		}
	}

	// A full hash cannot be confirmed by ls-remote on most servers, so it is
	// accepted as given; a wrong hash fails later at fetch time with a clear
	// git error.
	if IsCommitish(ref) {
		if len(ref) == 40 {
			return Resolution{Commit: strings.ToLower(ref), Requested: ref}, nil
		}
		// An abbreviated hash cannot be expanded without the objects.
		for _, r := range refs {
			if strings.HasPrefix(r.Hash, strings.ToLower(ref)) {
				return Resolution{Commit: r.Hash, Requested: ref}, nil
			}
		}
		return Resolution{}, fmt.Errorf("abbreviated commit %q does not match any ref tip; give the full 40-character hash", ref)
	}
	return Resolution{}, fmt.Errorf("ref %q not found: it is not a tag, branch or commit in this repository", ref)
}

// Tags returns the repository's tag names.
//
// Sorted with git's own version-aware comparison purely so that a human picking
// from the list sees recent releases first. cppm does not use this order to
// choose anything.
func (c *Client) Tags(repoURL string) ([]string, error) {
	refs, err := c.LsRemote(repoURL)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var tags []string
	for _, r := range refs {
		name := strings.TrimSuffix(r.Name, "^{}")
		if !strings.HasPrefix(name, "refs/tags/") {
			continue
		}
		t := strings.TrimPrefix(name, "refs/tags/")
		if !seen[t] {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	sort.Sort(sort.Reverse(byVersionish(tags)))
	return tags, nil
}

// DefaultBranch reports the remote's HEAD branch and its commit.
func (c *Client) DefaultBranch(repoURL string) (Resolution, error) {
	if err := validateURL(repoURL); err != nil {
		return Resolution{}, err
	}
	out, err := c.run("", "ls-remote", "--symref", "--", repoURL, "HEAD")
	if err != nil {
		return Resolution{}, err
	}
	var branch, hash string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			branch = fields[1]
		}
		if len(fields) == 2 && fields[1] == "HEAD" {
			hash = fields[0]
		}
	}
	if hash == "" {
		return Resolution{}, fmt.Errorf("%s: could not determine the default branch", repoURL)
	}
	if branch == "" {
		branch = "HEAD"
	}
	return Resolution{Ref: branch, Commit: hash, Requested: ""}, nil
}

// ErrShallowUnsupported reports that the server refused to serve a single
// commit, so the whole history has to be fetched instead.
var ErrShallowUnsupported = errors.New("server does not support fetching a single commit")

// FetchSource materialises one commit's working tree at dest.
//
// Shallow first, because a depth-1 fetch of a pinned commit is a fraction of the
// download. That relies on the server enabling uploadpack.allowReachableSHA1InWant,
// which GitHub does and many self-hosted servers do not, so failure falls back
// to a full fetch rather than being fatal. Whether shallow worked is reported so
// the lockfile can avoid re-probing the same host.
func (c *Client) FetchSource(repoURL, commit, dest string) (shallow bool, err error) {
	if err := validateURL(repoURL); err != nil {
		return false, err
	}
	if !IsCommitish(commit) || len(commit) != 40 {
		return false, fmt.Errorf("fetch needs a full 40-character commit, got %q", commit)
	}

	// The cache is keyed by commit, so an existing complete checkout is correct
	// by construction and can be reused.
	if ok, _ := isCheckout(dest, commit); ok {
		return true, nil
	}
	if err := os.RemoveAll(dest); err != nil {
		return false, err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return false, err
	}

	cleanup := func() {
		_ = os.RemoveAll(dest)
	}

	if _, err := c.run(dest, "init", "--quiet"); err != nil {
		cleanup()
		return false, err
	}
	if _, err := c.run(dest, "remote", "add", "origin", "--", repoURL); err != nil {
		cleanup()
		return false, err
	}

	shallowErr := func() error {
		_, err := c.run(dest, "fetch", "--quiet", "--depth", "1", "origin", commit)
		return err
	}()

	if shallowErr != nil {
		// Full history, then check out the commit by hash.
		if _, err := c.run(dest, "fetch", "--quiet", "--tags", "origin"); err != nil {
			cleanup()
			return false, fmt.Errorf("shallow fetch failed (%v) and full fetch also failed: %w", shallowErr, err)
		}
		if _, err := c.run(dest, "checkout", "--quiet", "--detach", commit); err != nil {
			cleanup()
			return false, err
		}
		return false, nil
	}

	if _, err := c.run(dest, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		cleanup()
		return false, err
	}
	return true, nil
}

// Submodule is one entry from a checkout's .gitmodules.
type Submodule struct {
	// Path is relative to the checkout root, slash-separated.
	Path string
	// URL is where git would clone it from. Reported to the user because the
	// host is the actionable part when a fetch fails.
	URL string
}

// Submodules reports what a checkout declares and what is actually there.
type Submodules struct {
	Declared []Submodule
	// Missing are declared but still empty after the attempt, because the fetch
	// failed. Not an error: see InitSubmodules.
	Missing []Submodule
}

// InitSubmodules populates a checkout's submodules, best effort.
//
// Best effort is the whole point, and it is not laziness. A failure here must
// not stop `cppm add`: the fetch exists to inspect CMake files, and a
// dependency whose submodule is unreachable is still worth adding -- the user
// may be behind a proxy that CMake will later be given, or the submodule may
// hold nothing but that package's own test fixtures. What the caller gets
// instead is the list of what is missing, which is enough to warn that the
// build will try the same fetch and may fail the same way.
//
// Submodules are fetched at depth 1 first, then fully, mirroring FetchSource:
// a submodule is pinned to a commit that is often not any branch's tip, and
// not every server allows fetching such a commit shallowly.
func (c *Client) InitSubmodules(dest string) (Submodules, error) {
	var out Submodules

	if _, err := os.Stat(filepath.Join(dest, ".gitmodules")); err != nil {
		return out, nil // the overwhelmingly common case
	}
	declared, err := c.declaredSubmodules(dest)
	if err != nil {
		return out, err
	}
	out.Declared = declared
	if len(declared) == 0 {
		return out, nil
	}

	if err := c.updateSubmodules(dest, false); err != nil {
		// `submodule update` stops at the first failure and leaves the rest
		// untouched, so one unreachable submodule costs all the others. drogon
		// is exactly this: third_party/eric-hpack-core is unreachable from some
		// networks and sorts ahead of trantor, which is the one whose CMake
		// discovery actually needs to resolve. Retrying one at a time means a
		// single bad entry only costs itself.
		for _, s := range declared {
			_ = c.updateSubmodules(dest, true, s.Path)
		}
	}

	for _, s := range declared {
		if !isPopulated(filepath.Join(dest, filepath.FromSlash(s.Path))) {
			out.Missing = append(out.Missing, s)
		}
	}
	return out, nil
}

// updateSubmodules runs one `submodule update`, shallow first then full.
//
// Shallow fails when the pinned commit is not at a branch tip and the server
// does not allow fetching an arbitrary commit, which is the same hazard
// FetchSource handles for the superproject.
//
// paths, when given, limit the update. They are passed after "--" because a
// path from .gitmodules is upstream's text, not cppm's: git must read it as a
// path and never as an option.
//
// force is needed for the per-path retry, and the reason is subtle enough to
// be worth stating. When a bulk update aborts partway, the submodules it had
// already cloned are left checked out at nothing -- git clones them with no
// worktree first and performs the checkouts afterwards, which never happens.
// Their recorded HEAD is already correct, so a plain retry exits 0 having done
// nothing and the directory stays empty. --force makes git check out the
// commit regardless. Discarding "local changes" is safe here because this
// checkout is cppm's commit-keyed cache, which nothing edits.
func (c *Client) updateSubmodules(dest string, force bool, paths ...string) error {
	base := []string{"submodule", "update", "--init", "--recursive"}
	if force {
		base = append(base, "--force")
	}
	var tail []string
	if len(paths) > 0 {
		tail = append([]string{"--"}, paths...)
	}

	shallow := append(append(append([]string{}, base...), "--depth", "1"), tail...)
	if _, err := c.run(dest, shallow...); err == nil {
		return nil
	}
	full := append(append([]string{}, base...), tail...)
	_, err := c.run(dest, full...)
	return err
}

// declaredSubmodules reads .gitmodules with git itself rather than parsing it.
//
// The file is git config syntax, which has escaping rules not worth
// reimplementing, and `git config --file` cannot execute anything from it.
func (c *Client) declaredSubmodules(dest string) ([]Submodule, error) {
	out, err := c.run(dest, "config", "--file", ".gitmodules", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		// A .gitmodules with no path entries is malformed upstream, not a cppm
		// problem, and nothing can be fetched from it.
		return nil, nil
	}
	var subs []Submodule
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		key, path, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || path == "" {
			continue
		}
		// A path that climbs out of the checkout would have git write outside
		// the cache directory. git refuses this itself, but cppm hands these
		// paths to its own file walks, so it must not trust them either.
		// A leading "-" would be read as an option by any git command that
		// takes this path, which is why every call site also uses "--".
		if filepath.IsAbs(path) || strings.HasPrefix(path, "-") ||
			path == ".." || strings.HasPrefix(path, "../") ||
			strings.Contains(path, "/../") || strings.HasSuffix(path, "/..") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "submodule."), ".path")
		url, _ := c.run(dest, "config", "--file", ".gitmodules", "--get", "submodule."+name+".url")
		subs = append(subs, Submodule{
			Path: filepath.ToSlash(path),
			URL:  strings.TrimSpace(url),
		})
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].Path < subs[j].Path })
	return subs, nil
}

// isPopulated reports whether dir holds anything. A submodule whose fetch
// failed is left as an empty directory.
func isPopulated(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// isCheckout reports whether dest already holds the given commit checked out.
func isCheckout(dest, commit string) (bool, error) {
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		return false, err
	}
	c := &Client{}
	out, err := c.run(dest, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == commit, nil
}

// CacheDir is the root of cppm's global cache.
//
// Global rather than per-project: ten projects using fmt should not mean ten
// clones of fmt.
func CacheDir() string {
	if v := os.Getenv("CPPM_CACHE_DIR"); v != "" {
		return v
	}
	if base, err := os.UserCacheDir(); err == nil {
		return filepath.Join(base, "cppm")
	}
	return filepath.Join(os.TempDir(), "cppm-cache")
}

// SourceDir is the cache location for one commit of one repository.
//
// Keyed by commit, so entries are immutable and never need invalidating.
func SourceDir(repoURL, commit string) string {
	host, path := urlParts(repoURL)
	short := commit
	if len(short) > 12 {
		short = short[:12]
	}
	return filepath.Join(CacheDir(), "src", host, filepath.FromSlash(path), short)
}

// urlParts splits a repository URL into a host and a filesystem-safe path, for
// use as cache directory names.
func urlParts(repoURL string) (host, path string) {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" {
		// scp-style "git@host:owner/repo.git"
		s := repoURL
		if i := strings.Index(s, "@"); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.Index(s, ":"); i >= 0 {
			return sanitizeSegment(s[:i]), sanitizePath(strings.TrimSuffix(s[i+1:], ".git"))
		}
		return "unknown", sanitizePath(strings.TrimSuffix(s, ".git"))
	}
	return sanitizeSegment(u.Host), sanitizePath(strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git"))
}

func sanitizeSegment(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.' || r == '-' || r == '_':
			return r
		default:
			return '_'
		}
	}, s)
	if s == "" {
		return "unknown"
	}
	return s
}

func sanitizePath(p string) string {
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			continue
		}
		out = append(out, sanitizeSegment(part))
	}
	if len(out) == 0 {
		return "unknown"
	}
	return strings.Join(out, "/")
}

// validateURL rejects URLs git would misread or that cppm will not fetch.
func validateURL(repoURL string) error {
	if repoURL == "" {
		return errors.New("empty repository URL")
	}
	if strings.HasPrefix(repoURL, "-") {
		return fmt.Errorf("invalid URL %q: must not begin with '-'", repoURL)
	}
	if strings.ContainsAny(repoURL, "\n\r") {
		return fmt.Errorf("invalid URL %q: contains a newline", repoURL)
	}
	// "ext::" and other transport helpers run arbitrary commands.
	for _, bad := range []string{"ext::", "fd::"} {
		if strings.HasPrefix(strings.ToLower(repoURL), bad) {
			return fmt.Errorf("refusing URL %q: the %q transport can execute arbitrary commands", repoURL, strings.TrimSuffix(bad, "::"))
		}
	}
	return nil
}

// byVersionish orders tags so that a human sees plausible newest-first, by
// comparing embedded digit runs numerically. Display only.
type byVersionish []string

func (s byVersionish) Len() int      { return len(s) }
func (s byVersionish) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s byVersionish) Less(i, j int) bool {
	return compareVersionish(s[i], s[j]) < 0
}

func compareVersionish(a, b string) int {
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		if isDigit(a[ai]) && isDigit(b[bi]) {
			as, bs := ai, bi
			for ai < len(a) && isDigit(a[ai]) {
				ai++
			}
			for bi < len(b) && isDigit(b[bi]) {
				bi++
			}
			an := strings.TrimLeft(a[as:ai], "0")
			bn := strings.TrimLeft(b[bs:bi], "0")
			if len(an) != len(bn) {
				if len(an) < len(bn) {
					return -1
				}
				return 1
			}
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
			continue
		}
		if a[ai] != b[bi] {
			if a[ai] < b[bi] {
				return -1
			}
			return 1
		}
		ai++
		bi++
	}
	switch {
	case len(a)-ai < len(b)-bi:
		return -1
	case len(a)-ai > len(b)-bi:
		return 1
	}
	return 0
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
