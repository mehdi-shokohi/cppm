package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scenario install exists for: a clone that carries cppm.json and nothing
// generated. install has to produce every generated file and a configured
// build tree from the manifest alone.
func TestInstallSetsUpAFreshCloneFromTheManifest(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cmake")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "CloneApp")
	proj := filepath.Join(dir, "CloneApp")

	// Simulate the clone: keep what a cppm project commits, drop what it does
	// not. cppm.lock is kept because committing it is the documented practice.
	for _, gone := range []string{".cppm", "build"} {
		if err := os.RemoveAll(filepath.Join(proj, gone)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(proj, "CMakePresets.json")); err != nil {
		t.Fatal(err)
	}

	mustRun(t, proj, "install")

	for _, f := range []string{
		"CMakePresets.json",
		".cppm/generated/cppm.cmake",
		"build/debug/CMakeCache.txt", // configured, which is what downloads deps
	} {
		if _, err := os.Stat(filepath.Join(proj, f)); err != nil {
			t.Errorf("install did not produce %s", f)
		}
	}

	// install configures but deliberately does not compile.
	if _, err := os.Stat(filepath.Join(proj, "build", "debug", exeName("CloneApp"))); err == nil {
		t.Error("install must not build; that is 'cppm build'")
	}
	mustRun(t, proj, "build")
}

// A clone with no lockfile at all: install resolves the gaps and writes one.
func TestInstallWritesAMissingLockfile(t *testing.T) {
	network(t)
	withCache(t)
	_, proj := initProject(t, "NoLockApp")

	mustRun(t, proj, "add", "github:fmtlib/fmt@11.1.4")
	if err := os.Remove(filepath.Join(proj, "cppm.lock")); err != nil {
		t.Fatal(err)
	}

	out := mustRun(t, proj, "install", "--no-configure")
	if !strings.Contains(out, "not in cppm.lock") {
		t.Errorf("install did not report the unresolved dependency:\n%s", out)
	}

	lock := readFile(t, filepath.Join(proj, "cppm.lock"))
	if !strings.Contains(lock, "123913715afeb8a437e6388b4473fcc4753e1c9a") {
		t.Errorf("install did not pin the resolved commit:\n%s", lock)
	}
	// The target name has to come back too, or cppm_link_dependencies links
	// nothing and the build is silently wrong.
	if !strings.Contains(lock, `"fmt::fmt"`) {
		t.Errorf("install did not record the dependency's target:\n%s", lock)
	}
}

// Hard constraint: only `cppm update` moves a pin. If install re-resolved, a
// clone would build different code from the one that committed the lockfile,
// which is the whole reason the lockfile exists.
func TestInstallNeverMovesAPin(t *testing.T) {
	network(t)
	withCache(t)
	_, proj := initProject(t, "PinnedApp")

	mustRun(t, proj, "add", "github:fmtlib/fmt@11.1.4")
	before := readFile(t, filepath.Join(proj, "cppm.lock"))

	// The manifest now asks for a different ref than the lockfile pins. install
	// must obey the lockfile and leave the disagreement for doctor to report.
	mf := filepath.Join(proj, "cppm.json")
	writeFile(t, mf, strings.Replace(readFile(t, mf), `"version": "11.1.4"`, `"version": "10.2.1"`, 1))

	mustRun(t, proj, "install", "--no-configure")

	if after := readFile(t, filepath.Join(proj, "cppm.lock")); after != before {
		t.Errorf("install moved a pin.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestInstallNoConfigureTouchesNothingElse(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "QuietApp")
	proj := filepath.Join(dir, "QuietApp")

	cml := readFile(t, filepath.Join(proj, "CMakeLists.txt"))
	out := mustRun(t, proj, "install", "--no-configure")

	if !strings.Contains(out, "nothing was downloaded") {
		t.Errorf("--no-configure should say it stopped early:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(proj, "build")); err == nil {
		t.Error("--no-configure must not create a build tree")
	}
	if got := readFile(t, filepath.Join(proj, "CMakeLists.txt")); got != cml {
		t.Error("install modified the user-owned CMakeLists.txt")
	}
}

func TestInstallOutsideAProjectFailsClearly(t *testing.T) {
	out, err := run(t, t.TempDir(), "install")
	if err == nil {
		t.Fatalf("install outside a project should fail:\n%s", out)
	}
	if !strings.Contains(out, "cppm.json") {
		t.Errorf("the error should name cppm.json:\n%s", out)
	}
}
