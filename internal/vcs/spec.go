// Package vcs resolves dependency specifications and fetches sources with git.
//
// Everything here runs the git CLI through an argv slice, never a shell. Two
// further injection risks are handled explicitly: a ref or URL beginning with
// "-" would be read by git as a flag, and a local path could escape the project
// with "..". Both are rejected rather than escaped.
package vcs

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Spec is a parsed dependency specification.
type Spec struct {
	// Name is the dependency's key in cppm.json, derived from the repository
	// unless overridden.
	Name string
	// Source is "git" or "path".
	Source string
	// URL is the clone URL for git sources.
	URL string
	// Path is the directory for path sources.
	Path string
	// Ref is the requested git ref: a tag, branch or commit. Empty means the
	// caller must choose, since cppm does not interpret versions.
	Ref string
}

// hostShorthands maps the "<host>:owner/repo" prefixes to their base URLs.
//
// These are a CLI convenience only. The manifest stores a full URL, so the
// schema stays host-agnostic and self-hosted git works with no special case.
var hostShorthands = map[string]string{
	"github":    "https://github.com/",
	"gitlab":    "https://gitlab.com/",
	"codeberg":  "https://codeberg.org/",
	"bitbucket": "https://bitbucket.org/",
	"sr.ht":     "https://git.sr.ht/~",
}

var (
	ownerRepoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*/[A-Za-z0-9][A-Za-z0-9._~-]*$`)
	nameRe      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)
	hexRe       = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
)

// ParseSpec parses a dependency specification.
//
//	github:fmtlib/fmt            latest - the caller decides what that means
//	github:fmtlib/fmt@11.1.4     tag, branch or commit
//	gitlab:libeigen/eigen@3.4.0
//	https://example.com/a/b.git@main
//	local:../MyLibrary
//	path:../MyLibrary            synonym for local:
func ParseSpec(raw string) (Spec, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Spec{}, fmt.Errorf("empty dependency specification")
	}

	// Split off @ref, but not the @ in a scp-style or userinfo URL.
	body, ref := s, ""
	if i := strings.LastIndex(s, "@"); i > 0 && !strings.Contains(s[i:], "/") {
		body, ref = s[:i], s[i+1:]
	}
	if err := validateRef(ref); err != nil {
		return Spec{}, err
	}

	scheme, rest := "", body
	if i := strings.Index(body, ":"); i > 0 {
		scheme, rest = strings.ToLower(body[:i]), body[i+1:]
	}

	switch scheme {
	case "local", "path", "file":
		p := strings.TrimSpace(rest)
		if p == "" {
			return Spec{}, fmt.Errorf("%q: no path given", raw)
		}
		if ref != "" {
			return Spec{}, fmt.Errorf("%q: a path dependency cannot have a ref; it is editable by design", raw)
		}
		if strings.HasPrefix(p, "-") {
			return Spec{}, fmt.Errorf("%q: path must not begin with '-'", raw)
		}
		return Spec{Name: deriveName(path.Base(strings.TrimRight(toSlash(p), "/"))), Source: "path", Path: p}, nil

	case "http", "https", "ssh", "git":
		u, err := url.Parse(body)
		if err != nil {
			return Spec{}, fmt.Errorf("%q: %w", raw, err)
		}
		if u.Host == "" {
			return Spec{}, fmt.Errorf("%q: no host in URL", raw)
		}
		return Spec{Name: deriveName(path.Base(u.Path)), Source: "git", URL: body, Ref: ref}, nil
	}

	if base, ok := hostShorthands[scheme]; ok {
		if !ownerRepoRe.MatchString(rest) {
			return Spec{}, fmt.Errorf("%q: expected %s:<owner>/<repo>", raw, scheme)
		}
		return Spec{
			Name:   deriveName(path.Base(rest)),
			Source: "git",
			URL:    base + rest + ".git",
			Ref:    ref,
		}, nil
	}

	if scheme != "" {
		return Spec{}, fmt.Errorf("%q: unknown source %q (expected github, gitlab, codeberg, bitbucket, local, or a full URL)", raw, scheme)
	}

	// Bare owner/repo defaults to GitHub, which is where most C++ libraries are.
	if ownerRepoRe.MatchString(rest) {
		return Spec{
			Name:   deriveName(path.Base(rest)),
			Source: "git",
			URL:    hostShorthands["github"] + rest + ".git",
			Ref:    ref,
		}, nil
	}
	return Spec{}, fmt.Errorf("%q: unrecognised specification (try github:owner/repo or local:../path)", raw)
}

// validateRef rejects refs that git would misread as flags, and the characters
// git refuses in a ref name anyway.
func validateRef(ref string) error {
	if ref == "" {
		return nil
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid ref %q: must not begin with '-'", ref)
	}
	if strings.ContainsAny(ref, " \t\n\\^~:?*[") {
		return fmt.Errorf("invalid ref %q: contains a character git does not allow in a ref name", ref)
	}
	if strings.Contains(ref, "..") {
		return fmt.Errorf("invalid ref %q: must not contain '..'", ref)
	}
	return nil
}

// IsCommitish reports whether a ref looks like a raw object name.
func IsCommitish(ref string) bool { return hexRe.MatchString(ref) }

// deriveName turns a repository basename into a dependency name.
//
// "fmt.git" -> "fmt", "abseil-cpp" -> "abseil-cpp". A name that cannot be made
// valid is returned as-is so the caller can report it and suggest --name.
func deriveName(base string) string {
	base = strings.TrimSuffix(base, ".git")
	base = strings.TrimPrefix(base, "~")
	if base == "" {
		return ""
	}
	if nameRe.MatchString(base) {
		return base
	}
	cleaned := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(base, "_")
	cleaned = strings.Trim(cleaned, "_.-")
	if cleaned == "" || !nameRe.MatchString(cleaned) {
		return base
	}
	return cleaned
}

// ValidName reports whether a dependency name is usable.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// toSlash normalises a spec's path separators; specs are always written with
// forward slashes regardless of platform.
func toSlash(p string) string { return strings.ReplaceAll(p, `\`, "/") }
