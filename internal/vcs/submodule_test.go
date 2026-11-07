package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git refuses file:// submodule transport by default (CVE-2022-39253). These
// tests build their fixtures locally, so they have to opt back in -- through
// the environment rather than the user's config, since Client.env() inherits
// os.Environ() and nothing here may touch a developer's real git setup.
func allowFileSubmodules(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// repoWithSubmodule builds a superproject holding one good submodule, plus
// optionally one whose URL does not exist. Returns the superproject path.
func repoWithSubmodule(t *testing.T, withBroken bool) string {
	t.Helper()
	root := t.TempDir()

	inner := filepath.Join(root, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, inner, "init", "-q", ".")
	if err := os.WriteFile(filepath.Join(inner, "CMakeLists.txt"),
		[]byte("add_library(inner STATIC inner.cpp)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, inner, "add", "-A")
	git(t, inner, "commit", "-qm", "inner")

	super := filepath.Join(root, "super")
	if err := os.MkdirAll(super, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, super, "init", "-q", ".")
	if err := os.WriteFile(filepath.Join(super, "CMakeLists.txt"),
		[]byte("add_subdirectory(vendor/inner)\nadd_library(super)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", "../inner", "vendor/inner")
	if withBroken {
		git(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", "../inner", "vendor/gone")
		// Repoint it at something that cannot resolve, which is what an
		// unreachable or deleted upstream looks like.
		git(t, super, "config", "--file", ".gitmodules", "submodule.vendor/gone.url",
			"https://invalid.invalid/nope.git")
	}
	git(t, super, "add", "-A")
	git(t, super, "commit", "-qm", "super")

	// Make the fixture look like what cppm actually inspects: a fresh checkout
	// where no submodule is initialised. Three things have to go, and each one
	// alone is enough to make these tests pass for the wrong reason --
	// `submodule add` caches the URL in .git/config (so .gitmodules is never
	// consulted), .git/modules keeps the object store (so git repopulates with
	// no network), and a populated worktree means nothing is attempted at all.
	git(t, super, "submodule", "deinit", "-f", "--all")
	for _, gone := range []string{filepath.Join(".git", "modules"), "vendor"} {
		if err := os.RemoveAll(filepath.Join(super, gone)); err != nil {
			t.Fatal(err)
		}
	}
	return super
}

func TestInitSubmodulesPopulatesThem(t *testing.T) {
	requireGit(t)
	allowFileSubmodules(t)
	super := repoWithSubmodule(t, false)

	c := &Client{}
	got, err := c.InitSubmodules(super)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Declared) != 1 || got.Declared[0].Path != "vendor/inner" {
		t.Fatalf("declared = %+v, want one entry at vendor/inner", got.Declared)
	}
	if len(got.Missing) != 0 {
		t.Errorf("missing = %+v, want none", got.Missing)
	}
	if _, err := os.Stat(filepath.Join(super, "vendor", "inner", "CMakeLists.txt")); err != nil {
		t.Error("the submodule's CMakeLists.txt was not checked out")
	}
}

// The drogon case: one submodule's host answers 403, and `cppm add` must still
// succeed while saying which one failed. A hard error here would make a
// dependency unaddable because of a network cppm cannot see.
func TestInitSubmodulesReportsAnUnreachableOneWithoutFailing(t *testing.T) {
	requireGit(t)
	allowFileSubmodules(t)
	super := repoWithSubmodule(t, true)

	c := &Client{}
	got, err := c.InitSubmodules(super)
	if err != nil {
		t.Fatalf("an unreachable submodule must not be an error: %v", err)
	}
	if len(got.Declared) != 2 {
		t.Fatalf("declared = %+v, want two", got.Declared)
	}
	if len(got.Missing) != 1 || got.Missing[0].Path != "vendor/gone" {
		t.Fatalf("missing = %+v, want only vendor/gone", got.Missing)
	}
	if got.Missing[0].URL != "https://invalid.invalid/nope.git" {
		t.Errorf("missing URL = %q; the host is the actionable part, so it must be reported",
			got.Missing[0].URL)
	}
	// The reachable one must still have been fetched: a failure anywhere in the
	// list cannot be allowed to abandon the rest.
	if _, err := os.Stat(filepath.Join(super, "vendor", "inner", "CMakeLists.txt")); err != nil {
		t.Error("the reachable submodule was not checked out")
	}
}

func TestInitSubmodulesOnARepoWithoutAny(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", ".")
	if err := os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte("add_library(x)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "x")

	got, err := c0().InitSubmodules(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Declared) != 0 || len(got.Missing) != 0 {
		t.Errorf("got %+v, want nothing declared", got)
	}
}

// A .gitmodules path that climbs out of the checkout would make cppm walk, and
// report on, directories outside its cache.
func TestInitSubmodulesIgnoresEscapingPaths(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", ".")
	if err := os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte(
		"[submodule \"evil\"]\n\tpath = ../../outside\n\turl = https://example.invalid/e.git\n"+
			"[submodule \"abs\"]\n\tpath = /etc\n\turl = https://example.invalid/a.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := c0().InitSubmodules(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Declared) != 0 {
		t.Errorf("declared = %+v, want the escaping paths dropped", got.Declared)
	}
}

func c0() *Client { return &Client{} }
