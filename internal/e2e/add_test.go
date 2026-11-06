package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// network gates tests that reach GitHub, so the suite is usable offline.
func network(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("needs the network")
	}
	if os.Getenv("CPPM_SKIP_NETWORK_TESTS") != "" {
		t.Skip("CPPM_SKIP_NETWORK_TESTS is set")
	}
	requireTool(t, "git")
	cmd := exec.Command("git", "ls-remote", "--heads", "https://github.com/fmtlib/fmt.git")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err := cmd.Run(); err != nil {
		t.Skip("no network access to github.com")
	}
}

// withCache points the global cache at the test's temp dir, so tests neither
// pollute nor depend on the developer's real cache.
func withCache(t *testing.T) {
	t.Helper()
	t.Setenv("CPPM_CACHE_DIR", t.TempDir())
}

func initProject(t *testing.T, name string) (dir, proj string) {
	t.Helper()
	dir = t.TempDir()
	mustRun(t, dir, "init", name)
	return dir, filepath.Join(dir, name)
}

func TestAddResolvesTagToCommitAndPins(t *testing.T) {
	network(t)
	withCache(t)
	_, proj := initProject(t, "App")

	out := mustRun(t, proj, "add", "github:fmtlib/fmt@11.1.4")
	if !strings.Contains(out, "tag 11.1.4") {
		t.Errorf("expected the tag to be reported: %s", out)
	}

	lock := readFile(t, filepath.Join(proj, "cppm.lock"))
	// The commit must be the real one for this tag: a tag alone is not
	// reproducible, since tags move.
	if !strings.Contains(lock, "123913715afeb8a437e6388b4473fcc4753e1c9a") {
		t.Errorf("lockfile does not pin the resolved commit:\n%s", lock)
	}
	if !strings.Contains(lock, `"fmt::fmt"`) {
		t.Errorf("lockfile does not record the target:\n%s", lock)
	}

	m := readFile(t, filepath.Join(proj, "cppm.json"))
	if !strings.Contains(m, `"version": "11.1.4"`) {
		t.Errorf("manifest does not record the requested ref:\n%s", m)
	}
	// A resolved commit belongs in the lock, not the manifest: the manifest is
	// intent.
	if strings.Contains(m, "123913715afeb") {
		t.Error("the manifest must not contain a resolved commit")
	}
}

func TestAddThenBuildLinksTheDependency(t *testing.T) {
	network(t)
	withCache(t)
	requireTool(t, "cmake")
	_, proj := initProject(t, "App")

	mustRun(t, proj, "add", "github:fmtlib/fmt@11.1.4")

	src := filepath.Join(proj, "src", "main.cpp")
	prog := "#include <fmt/core.h>\nint main(){ fmt::print(\"linked ok\\n\"); }\n"
	if err := os.WriteFile(src, []byte(prog), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, proj, "build")

	exe := filepath.Join(proj, "build", "debug", "App")
	got, err := exec.Command(exe).CombinedOutput()
	if err != nil {
		t.Fatalf("built binary failed: %v\n%s", err, got)
	}
	if !strings.Contains(string(got), "linked ok") {
		t.Errorf("unexpected output: %q", got)
	}
}

// A library with no recipe must be resolved by scanning its own CMake.
func TestAddDiscoversTargetWithoutARecipe(t *testing.T) {
	network(t)
	withCache(t)
	_, proj := initProject(t, "App")

	out := mustRun(t, proj, "add", "github:p-ranav/argparse@v3.1", "--non-interactive")
	if !strings.Contains(out, "argparse::argparse") {
		t.Errorf("discovery did not find argparse::argparse: %s", out)
	}
	if !strings.Contains(out, "inferred") {
		t.Errorf("an inferred target must be reported as inferred, not presented as fact: %s", out)
	}
}

// --target skips discovery entirely and is written to the manifest, because an
// explicit choice is intent rather than a resolution detail.
func TestAddWithExplicitTarget(t *testing.T) {
	network(t)
	withCache(t)
	_, proj := initProject(t, "App")

	mustRun(t, proj, "add", "github:fmtlib/fmt@11.1.4", "--target", "fmt::fmt-header-only")
	m := readFile(t, filepath.Join(proj, "cppm.json"))
	if !strings.Contains(m, "fmt::fmt-header-only") {
		t.Errorf("explicit target not recorded in the manifest:\n%s", m)
	}
	out := mustRun(t, proj, "list", "--long")
	if !strings.Contains(out, "explicit") {
		t.Errorf("origin should be explicit: %s", out)
	}
}

func TestAddRejectsDuplicateWithoutForce(t *testing.T) {
	network(t)
	withCache(t)
	_, proj := initProject(t, "App")

	mustRun(t, proj, "add", "github:fmtlib/fmt@11.1.4")
	if _, err := run(t, proj, "add", "github:fmtlib/fmt@11.1.4"); err == nil {
		t.Error("adding the same dependency twice should fail without --force")
	}
	if _, err := run(t, proj, "add", "github:fmtlib/fmt@11.0.0", "--force"); err != nil {
		t.Error("--force should permit replacing a dependency")
	}
}

func TestRemoveDropsFromManifestLockAndGeneratedCMake(t *testing.T) {
	network(t)
	withCache(t)
	_, proj := initProject(t, "App")

	mustRun(t, proj, "add", "github:fmtlib/fmt@11.1.4")
	mustRun(t, proj, "remove", "fmt")

	for _, f := range []string{"cppm.json", "cppm.lock", ".cppm/generated/cppm.cmake"} {
		if strings.Contains(readFile(t, filepath.Join(proj, f)), "fmtlib/fmt") {
			t.Errorf("%s still references the removed dependency", f)
		}
	}
	if _, err := run(t, proj, "remove", "ghost"); err == nil {
		t.Error("removing an unknown dependency should fail")
	}
}

// update re-resolves refs. It must not claim to find newer releases, since cppm
// never compares versions.
func TestUpdateOnAPinnedTagIsANoOp(t *testing.T) {
	network(t)
	withCache(t)
	_, proj := initProject(t, "App")

	mustRun(t, proj, "add", "github:fmtlib/fmt@11.1.4")
	before := readFile(t, filepath.Join(proj, "cppm.lock"))

	out := mustRun(t, proj, "update")
	if !strings.Contains(out, "already at their resolved commits") {
		t.Errorf("a pinned tag should not move: %s", out)
	}
	if readFile(t, filepath.Join(proj, "cppm.lock")) != before {
		t.Error("update changed the lockfile for a pinned tag")
	}
}

func TestAddLocalPathDependency(t *testing.T) {
	withCache(t)
	dir, proj := initProject(t, "App")

	// A sibling library, outside the project.
	lib := filepath.Join(dir, "MyLib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	cml := "project(MyLib)\nadd_library(MyLib INTERFACE)\nadd_library(MyLib::MyLib ALIAS MyLib)\n"
	if err := os.WriteFile(filepath.Join(lib, "CMakeLists.txt"), []byte(cml), 0o644); err != nil {
		t.Fatal(err)
	}

	out := mustRun(t, proj, "add", "local:../MyLib")
	if !strings.Contains(out, "MyLib") {
		t.Errorf("unexpected output: %s", out)
	}
	gen := readFile(t, filepath.Join(proj, ".cppm", "generated", "cppm.cmake"))
	if !strings.Contains(gen, "add_subdirectory") {
		t.Error("a path dependency should use add_subdirectory, not FetchContent")
	}
	// Editable by design, so never pinned to a commit.
	if strings.Contains(readFile(t, filepath.Join(proj, "cppm.lock")), `"commit"`) {
		t.Error("a path dependency must not be pinned to a commit")
	}
}

func TestAddLocalRejectsBadPaths(t *testing.T) {
	withCache(t)
	dir, proj := initProject(t, "App")

	if _, err := run(t, proj, "add", "local:../does-not-exist"); err == nil {
		t.Error("a missing directory should fail")
	}
	// A directory with no CMakeLists.txt cannot be add_subdirectory'd.
	empty := filepath.Join(dir, "Empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, proj, "add", "local:../Empty"); err == nil {
		t.Error("a directory without CMakeLists.txt should fail")
	}
	// A directory inside the project belongs in add_subdirectory directly.
	inside := filepath.Join(proj, "sub")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "CMakeLists.txt"), []byte("project(x)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, proj, "add", "local:./sub"); err == nil {
		t.Error("a path inside the project should be refused with advice")
	}
}

func TestAddRejectsBadSpecs(t *testing.T) {
	withCache(t)
	_, proj := initProject(t, "App")
	for _, bad := range []string{
		"", "notaspec", "svn:a/b",
		"github:a/b@-upload-pack=evil",
		"ext::sh -c evil",
	} {
		if _, err := run(t, proj, "add", bad); err == nil {
			t.Errorf("add %q should have failed", bad)
		}
	}
}

// Prompting in a non-interactive context would hang a CI job forever.
func TestAddWithoutRefIsNonInteractiveSafe(t *testing.T) {
	network(t)
	withCache(t)
	_, proj := initProject(t, "App")

	out := mustRun(t, proj, "add", "github:fmtlib/fmt", "--non-interactive")
	if !strings.Contains(out, "default branch") {
		t.Errorf("expected the default branch to be used without a ref: %s", out)
	}
	if !strings.Contains(out, "pass @<tag>") {
		t.Errorf("should advise pinning a tag: %s", out)
	}
}

func TestInfoShowsResolutionProvenance(t *testing.T) {
	network(t)
	withCache(t)
	_, proj := initProject(t, "App")

	mustRun(t, proj, "add", "github:fmtlib/fmt@11.1.4")
	out := mustRun(t, proj, "info", "fmt")
	for _, want := range []string{"fmt", "11.1.4", "refs/tags/11.1.4", "recipe"} {
		if !strings.Contains(out, want) {
			t.Errorf("info output missing %q:\n%s", want, out)
		}
	}
}

// The cache is keyed by commit, so a second add of the same commit must not
// re-download.
func TestSourceCacheIsReused(t *testing.T) {
	network(t)
	cache := t.TempDir()
	t.Setenv("CPPM_CACHE_DIR", cache)

	_, p1 := initProject(t, "One")
	mustRun(t, p1, "add", "github:p-ranav/argparse@v3.1", "--non-interactive")

	entries, err := filepath.Glob(filepath.Join(cache, "src", "github.com", "p-ranav", "argparse", "*"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one cached commit, got %v (%v)", entries, err)
	}

	dir2 := t.TempDir()
	mustRun(t, dir2, "init", "Two")
	mustRun(t, filepath.Join(dir2, "Two"), "add", "github:p-ranav/argparse@v3.1", "--non-interactive")

	again, _ := filepath.Glob(filepath.Join(cache, "src", "github.com", "p-ranav", "argparse", "*"))
	if len(again) != 1 {
		t.Errorf("cache should be reused, not duplicated: %v", again)
	}
}
