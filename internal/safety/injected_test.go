package safety

import (
	"os"
	"path/filepath"
	"testing"
)

// duckdbLike reproduces the pattern that makes this scanner necessary: options
// that default ON and append a sanitizer to CMAKE_CXX_FLAGS_DEBUG, which is
// what duckdb does and what makes `cppm build --sanitize=tsan` fail in the
// compiler rather than in cppm.
func duckdbLike(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("CMakeLists.txt", `cmake_minimum_required(VERSION 3.24)
project(fakeduck CXX)

option(ENABLE_SANITIZER "Enable address sanitizer." TRUE)
option(ENABLE_UBSAN "Enable undefined behaviour sanitizer." TRUE)
option(ENABLE_THREAD_SANITIZER "Enable thread sanitizer." FALSE)

if(SOMETHING_ELSE)
  set(CMAKE_CXX_FLAGS_DEBUG "${CMAKE_CXX_FLAGS_DEBUG} -Wall")
endif()

if(ENABLE_SANITIZER)
  if(NOT MSVC)
    set(CMAKE_CXX_FLAGS_DEBUG "${CMAKE_CXX_FLAGS_DEBUG} -fsanitize=address")
  endif()
endif()

if (ENABLE_UBSAN)
    set(CMAKE_CXX_FLAGS_DEBUG "${CMAKE_CXX_FLAGS_DEBUG} -fsanitize=undefined")
endif()

# A comment mentioning -fsanitize=memory must not be reported.
`)
	write(filepath.Join("sub", "extra.cmake"), `set(CMAKE_CXX_FLAGS "${CMAKE_CXX_FLAGS} -fsanitize=address,undefined")
`)
	write("notcmake.txt", "-fsanitize=thread\n")
	return root
}

func TestScanFindsInjectedSanitizersAndNamesTheOption(t *testing.T) {
	got := ScanInjectedSanitizers("fakeduck", duckdbLike(t))
	if len(got) != 3 {
		t.Fatalf("expected 3 injections (asan, ubsan, and the .cmake file), got %d: %+v", len(got), got)
	}

	// The actionable half: the option the user sets OFF. An injection reported
	// without it leaves the user knowing they have a problem and not what to
	// do, which is the failure CLAUDE.md's "errors say what to do" forbids.
	byFlag := map[string]Injection{}
	for _, i := range got {
		for _, s := range i.Sanitizers {
			if _, seen := byFlag[s]; !seen {
				byFlag[s] = i
			}
		}
	}
	if o := byFlag["address"].Option; o != "ENABLE_SANITIZER" {
		t.Errorf("address injection attributed to option %q, want ENABLE_SANITIZER", o)
	}
	if !byFlag["address"].OptionDefaultsOn {
		t.Error("ENABLE_SANITIZER defaults ON, which is what makes this happen without being asked for")
	}
	if o := byFlag["undefined"].Option; o != "ENABLE_UBSAN" {
		t.Errorf("undefined injection attributed to option %q, want ENABLE_UBSAN", o)
	}

	// The nesting walk has to skip a closed if() block. SOMETHING_ELSE guards
	// a -Wall that has nothing to do with the sanitizer two blocks later.
	for _, i := range got {
		if i.Option == "SOMETHING_ELSE" {
			t.Errorf("%s:%d attributed to a closed if() block: the backwards walk is not nesting-aware", i.File, i.Line)
		}
		if i.Option == "MSVC" {
			t.Errorf("%s:%d attributed to MSVC, which is not an option anybody can set", i.File, i.Line)
		}
	}
}

// A comment is not an injection, and neither is a file CMake never reads.
// Reporting either would make the check cry wolf, and a diagnostic nobody
// believes is worse than none.
func TestScanIgnoresCommentsAndNonCMakeFiles(t *testing.T) {
	for _, i := range ScanInjectedSanitizers("fakeduck", duckdbLike(t)) {
		for _, s := range i.Sanitizers {
			if s == "memory" {
				t.Errorf("%s:%d reported a sanitizer named only in a comment", i.File, i.Line)
			}
			if s == "thread" {
				t.Errorf("%s:%d reported a flag from a file CMake does not read", i.File, i.Line)
			}
		}
	}
}

// Only the ASan/TSan pair cannot coexist. UBSan composes with either, so
// reporting it as a conflict would block a build that works.
func TestInjectionConflictsOnlyForTheIncompatiblePair(t *testing.T) {
	asan, _ := FindSanitizer(SanAddress)
	tsan, _ := FindSanitizer(SanThread)
	injectedAsan := Injection{Sanitizers: []string{"address"}}
	injectedUbsan := Injection{Sanitizers: []string{"undefined"}}
	injectedTsan := Injection{Sanitizers: []string{"thread"}}

	if !injectedAsan.Conflicts(tsan) {
		t.Error("a dependency injecting ASan must conflict with --sanitize=tsan")
	}
	if !injectedTsan.Conflicts(asan) {
		t.Error("a dependency injecting TSan must conflict with --sanitize=asan")
	}
	if injectedAsan.Conflicts(asan) {
		t.Error("a dependency injecting the sanitizer that was asked for is not a conflict")
	}
	if injectedUbsan.Conflicts(tsan) || injectedUbsan.Conflicts(asan) {
		t.Error("UBSan composes with both; reporting it as a conflict blocks a working build")
	}
}

// A tree that is not there means "cannot tell", not "clean". The scan runs
// before a sanitizer build and the sources may not be fetched yet.
func TestScanMissingTreeIsEmptyNotAnError(t *testing.T) {
	if got := ScanInjectedSanitizers("nope", filepath.Join(t.TempDir(), "does-not-exist")); len(got) != 0 {
		t.Errorf("expected no injections from a missing tree, got %+v", got)
	}
}
