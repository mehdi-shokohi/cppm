package e2e

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The claim that matters: a folder added with `cppm sources add` is really
// compiled and linked, without anything in CMakeLists.txt changing. A missing
// definition fails the link, so this cannot pass by accident.
func TestSourcesAddCompilesAndLinksASubfolder(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "FolderApp")
	proj := filepath.Join(dir, "FolderApp")

	writeFile(t, filepath.Join(proj, "include", "folderapp", "greet.hpp"),
		"#pragma once\n#include <string>\nstd::string greet();\nstd::string shout();\n")
	writeFile(t, filepath.Join(proj, "src", "net", "greet.cpp"),
		"#include \"folderapp/greet.hpp\"\nstd::string greet() { return \"from a subfolder\"; }\n")
	// A second level down, to prove the directory pattern is recursive.
	writeFile(t, filepath.Join(proj, "src", "net", "deep", "shout.cpp"),
		"#include \"folderapp/greet.hpp\"\nstd::string shout() { return \"and deeper\"; }\n")
	writeFile(t, filepath.Join(proj, "src", "main.cpp"),
		"#include <iostream>\n#include \"folderapp/greet.hpp\"\n"+
			"int main() { std::cout << greet() << ' ' << shout() << '\\n'; }\n")

	before := readFile(t, filepath.Join(proj, "CMakeLists.txt"))

	out := mustRun(t, proj, "sources", "add", "src/net")
	if !strings.Contains(out, "2 file(s)") {
		t.Errorf("expected both sources to be reported as matched:\n%s", out)
	}

	if after := readFile(t, filepath.Join(proj, "CMakeLists.txt")); after != before {
		t.Error("cppm sources must not touch the user-owned CMakeLists.txt")
	}

	mustRun(t, proj, "build")
	exe := filepath.Join(proj, "build", "debug", exeName("FolderApp"))
	got, err := exec.Command(exe).CombinedOutput()
	if err != nil {
		t.Fatalf("running the built binary failed: %v\n%s", err, got)
	}
	if !strings.Contains(string(got), "from a subfolder and deeper") {
		t.Errorf("unexpected program output: %q", got)
	}
}

// A file created after the last sync is picked up by `cppm build`, because
// build syncs first. This is the one behaviour the sync-time expansion trades
// away relative to file(GLOB CONFIGURE_DEPENDS), so it is worth proving that
// the usual workflow still works.
func TestSourcesPicksUpAFileAddedAfterTheLastSync(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real compiler")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "LaterApp")
	proj := filepath.Join(dir, "LaterApp")

	writeFile(t, filepath.Join(proj, "include", "laterapp", "parts.hpp"),
		"#pragma once\nint first();\nint second();\n")
	writeFile(t, filepath.Join(proj, "src", "parts", "first.cpp"),
		"#include \"laterapp/parts.hpp\"\nint first() { return 1; }\n")
	writeFile(t, filepath.Join(proj, "src", "main.cpp"),
		"#include <iostream>\n#include \"laterapp/parts.hpp\"\n"+
			"int main() { std::cout << first() + second() << '\\n'; }\n")

	mustRun(t, proj, "sources", "add", "src/parts")

	// second() is declared but not defined yet, so this must fail to link.
	if out, err := run(t, proj, "build"); err == nil {
		t.Fatalf("expected a link failure before second() exists:\n%s", out)
	}

	writeFile(t, filepath.Join(proj, "src", "parts", "second.cpp"),
		"#include \"laterapp/parts.hpp\"\nint second() { return 2; }\n")

	mustRun(t, proj, "build")
	exe := filepath.Join(proj, "build", "debug", exeName("LaterApp"))
	got, err := exec.Command(exe).CombinedOutput()
	if err != nil {
		t.Fatalf("running the built binary failed: %v\n%s", err, got)
	}
	if strings.TrimSpace(string(got)) != "3" {
		t.Errorf("program printed %q, want 3", got)
	}
}

func TestSourcesListReportsPatternsAndFiles(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "ListApp")
	proj := filepath.Join(dir, "ListApp")
	writeFile(t, filepath.Join(proj, "src", "net", "a.cpp"), "int a() { return 1; }\n")

	if out := mustRun(t, proj, "sources"); !strings.Contains(out, "no source patterns") {
		t.Errorf("a fresh project has no patterns:\n%s", out)
	}
	mustRun(t, proj, "sources", "add", "src/net")

	out := mustRun(t, proj, "sources")
	for _, want := range []string{"ListApp", "src/net", "src/net/a.cpp"} {
		if !strings.Contains(out, want) {
			t.Errorf("sources listing is missing %q:\n%s", want, out)
		}
	}
}

// src/main.cpp is listed in "sources" and so already named in CMakeLists.txt.
// Compiling it twice in one target is the kind of duplicate that produces
// confusing CMake output, so the pattern must skip it.
func TestSourcesSkipsFilesCMakeListsAlreadyNames(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "DupApp")
	proj := filepath.Join(dir, "DupApp")

	mustRun(t, proj, "sources", "add", "src")
	gen := readFile(t, filepath.Join(proj, ".cppm", "generated", "cppm.cmake"))
	if strings.Contains(gen, "src/main.cpp") {
		t.Error("src/main.cpp is in \"sources\" already; the glob must not add it again")
	}
}

func TestSourcesRemoveRestoresTheGeneratedFile(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "UndoApp")
	proj := filepath.Join(dir, "UndoApp")
	gen := filepath.Join(proj, ".cppm", "generated", "cppm.cmake")
	writeFile(t, filepath.Join(proj, "src", "net", "a.cpp"), "int a() { return 1; }\n")

	before := readFile(t, gen)
	mustRun(t, proj, "sources", "add", "src/net")
	if readFile(t, gen) == before {
		t.Fatal("adding a pattern did not change the generated CMake")
	}
	mustRun(t, proj, "sources", "remove", "src/net")
	if got := readFile(t, gen); got != before {
		t.Error("removing the pattern did not restore the generated CMake")
	}
}

func TestSourcesRejectsPatternsOutsideTheProject(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "SafeApp")
	proj := filepath.Join(dir, "SafeApp")

	for _, bad := range []string{"../elsewhere/*.cpp", "/etc/*.cpp"} {
		out, err := run(t, proj, "sources", "add", bad)
		if err == nil {
			t.Errorf("cppm sources add %q succeeded:\n%s", bad, out)
		}
	}
}

func TestSourcesWarnsWhenNothingMatchedYet(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "init", "EmptyApp")
	proj := filepath.Join(dir, "EmptyApp")

	out := mustRun(t, proj, "sources", "add", "src/typo/*.cpp")
	if !strings.Contains(out, "matched no files") {
		t.Errorf("a pattern matching nothing must say so:\n%s", out)
	}
}

// A target that cppm.json gives patterns to but CMakeLists.txt never defines
// must fail at configure time with an error that says what to do, rather than
// with CMake's own. This is the one way the deferred target_sources() breaks,
// and a user who added a target to cppm.json expecting cppm to create it will
// hit it.
func TestSourcesReportsATargetThatCMakeListsDoesNotDefine(t *testing.T) {
	if testing.Short() {
		t.Skip("runs cmake")
	}
	requireTool(t, "cmake")

	dir := t.TempDir()
	mustRun(t, dir, "init", "GoneApp")
	proj := filepath.Join(dir, "GoneApp")
	writeFile(t, filepath.Join(proj, "src", "net", "a.cpp"), "int a() { return 1; }\n")

	// A second target, declared in cppm.json only. cppm never writes a target
	// into CMakeLists.txt, so nothing defines it.
	mf := filepath.Join(proj, "cppm.json")
	edited := strings.Replace(readFile(t, mf), `"targets": {`,
		`"targets": {
    "Extra": { "kind": "library", "sourceGlobs": ["src/net"] },`, 1)
	writeFile(t, mf, edited)
	mustRun(t, proj, "sync")

	out, err := run(t, proj, "build")
	if err == nil {
		t.Fatalf("configure should have failed:\n%s", out)
	}
	for _, want := range []string{"sourceGlobs", "Extra", "cppm sources remove"} {
		if !strings.Contains(out, want) {
			t.Errorf("the failure does not mention %q:\n%s", want, out)
		}
	}
}
