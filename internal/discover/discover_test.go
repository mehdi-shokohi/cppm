package discover

import (
	"os"
	"path/filepath"
	"testing"
)

// tree writes a synthetic source tree, so these tests need no network.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The namespaced ALIAS is the name users link, and it is exactly what CMake
// will not enumerate. Recovering it from source is the whole point.
func TestFindsNamespacedAlias(t *testing.T) {
	root := tree(t, map[string]string{
		"CMakeLists.txt": `
project(fmt)
add_library(fmt STATIC src/format.cc)
add_library(fmt::fmt ALIAS fmt)
add_library(fmt-header-only INTERFACE)
add_library(fmt::fmt-header-only ALIAS fmt-header-only)
`,
	})
	res, err := Scan(root, "fmt")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Confident {
		t.Fatalf("expected confidence, got %q", res.Why)
	}
	if res.Best != "fmt::fmt" {
		t.Errorf("Best = %q, want fmt::fmt", res.Best)
	}
}

// Variable-built names are the dominant real-world case; resolving them moved
// measured accuracy from 60%% to 80%%.
func TestResolvesVariableBuiltNames(t *testing.T) {
	root := tree(t, map[string]string{
		"CMakeLists.txt": `
project(nlohmann_json VERSION 3.11.3 LANGUAGES CXX)
set(NLOHMANN_JSON_TARGET_NAME               ${PROJECT_NAME})
add_library(${NLOHMANN_JSON_TARGET_NAME} INTERFACE)
add_library(${PROJECT_NAME}::${NLOHMANN_JSON_TARGET_NAME} ALIAS ${NLOHMANN_JSON_TARGET_NAME})
`,
	})
	res, err := Scan(root, "json")
	if err != nil {
		t.Fatal(err)
	}
	if res.Best != "nlohmann_json::nlohmann_json" {
		t.Errorf("Best = %q, want nlohmann_json::nlohmann_json", res.Best)
	}
	if res.ProjectName != "nlohmann_json" {
		t.Errorf("ProjectName = %q", res.ProjectName)
	}
	// Reached only because the dependency's own project() name is used as a
	// second alias: "json" alone does not match.
	if !res.Confident {
		t.Errorf("expected confidence, got %q", res.Why)
	}
}

// abseil ranked absl::abseil_test_dll first and recommended it. A soft penalty
// was not enough, so these markers disqualify outright.
func TestVetoPreventsConfidentlyWrongAnswers(t *testing.T) {
	root := tree(t, map[string]string{
		"CMakeLists.txt": `
project(absl)
add_library(abseil_test_dll SHARED x.cc)
add_library(absl::abseil_test_dll ALIAS abseil_test_dll)
add_library(gtest STATIC y.cc)
add_library(GTest::gtest ALIAS gtest)
`,
	})
	res, err := Scan(root, "abseil")
	if err != nil {
		t.Fatal(err)
	}
	if res.Confident {
		t.Errorf("should have declined, but confidently chose %q", res.Best)
	}
	if len(res.Rejected) == 0 {
		t.Error("expected the vetoed candidates to be recorded for 'cppm info'")
	}
}

// A library whose own name contains a veto marker must not be vetoed by itself.
func TestVetoIgnoresMarkersInThePackageName(t *testing.T) {
	root := tree(t, map[string]string{
		"CMakeLists.txt": `
project(googletest)
add_library(gtest STATIC a.cc)
add_library(GTest::gtest ALIAS gtest)
`,
	})
	res, err := Scan(root, "googletest")
	if err != nil {
		t.Fatal(err)
	}
	if res.Best != "GTest::gtest" {
		t.Errorf("Best = %q, want GTest::gtest (the 'test' marker is in the package's own name)", res.Best)
	}
}

// Targets built by helper macros cannot be read by any scanner. Declining is
// the correct outcome; the recipe database covers these.
func TestDeclinesWhenTargetsComeFromHelperMacros(t *testing.T) {
	root := tree(t, map[string]string{
		"CMakeLists.txt":                        "project(googletest-distribution)\n",
		"googletest/CMakeLists.txt":             "cxx_library(gtest \"${cxx_strict}\" src/gtest-all.cc)\n",
		"googletest/cmake/internal_utils.cmake": "add_library(${cmake_package_name}::${name} ALIAS ${name})\n",
	})
	res, err := Scan(root, "googletest")
	if err != nil {
		t.Fatal(err)
	}
	if res.Confident {
		t.Errorf("should have declined, chose %q", res.Best)
	}
	if res.Why == "" {
		t.Error("a refusal must come with a reason")
	}
}

// Test and example subtrees never hold the public interface.
func TestIgnoresTestAndExampleDirectories(t *testing.T) {
	root := tree(t, map[string]string{
		"CMakeLists.txt":          "project(mylib)\nadd_library(mylib STATIC a.cc)\nadd_library(mylib::mylib ALIAS mylib)\n",
		"tests/CMakeLists.txt":    "add_library(mylib_testhelper STATIC h.cc)\nadd_library(mylib::testhelper ALIAS mylib_testhelper)\n",
		"examples/CMakeLists.txt": "add_library(demo STATIC d.cc)\n",
	})
	res, err := Scan(root, "mylib")
	if err != nil {
		t.Fatal(err)
	}
	if res.Best != "mylib::mylib" {
		t.Errorf("Best = %q, want mylib::mylib", res.Best)
	}
	for _, c := range res.Candidates {
		if c.Name == "demo" || c.Name == "mylib::testhelper" {
			t.Errorf("a candidate from a test or example directory survived: %s", c.Name)
		}
	}
}

// Two legitimate answers is a user decision, not an unknown.
func TestAmbiguityIsDetected(t *testing.T) {
	root := tree(t, map[string]string{
		"CMakeLists.txt": `
project(Catch2)
add_library(Catch2 STATIC a.cpp)
add_library(Catch2::Catch2 ALIAS Catch2)
add_library(Catch2WithMain STATIC b.cpp)
add_library(Catch2::Catch2WithMain ALIAS Catch2WithMain)
`,
	})
	res, err := Scan(root, "Catch2")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) < 2 {
		t.Fatalf("expected both Catch2 targets, got %d", len(res.Candidates))
	}
	if res.Best != "Catch2::Catch2" {
		t.Errorf("Best = %q", res.Best)
	}
}

// A bare target with no namespace is legitimate for some libraries.
func TestBareTargetIsAccepted(t *testing.T) {
	root := tree(t, map[string]string{
		"CMakeLists.txt": "project(glfw)\nadd_library(glfw STATIC a.c)\n",
	})
	res, err := Scan(root, "glfw")
	if err != nil {
		t.Fatal(err)
	}
	if res.Best != "glfw" {
		t.Errorf("Best = %q, want glfw", res.Best)
	}
}

func TestEmptyTreeDeclinesCleanly(t *testing.T) {
	res, err := Scan(tree(t, map[string]string{"README.md": "no cmake here"}), "whatever")
	if err != nil {
		t.Fatal(err)
	}
	if res.Confident || res.Best != "" {
		t.Errorf("expected a clean refusal, got %+v", res)
	}
	if res.Why == "" {
		t.Error("a refusal must explain itself")
	}
}

func TestScanIsDeterministic(t *testing.T) {
	files := map[string]string{
		"CMakeLists.txt": `
project(lib)
add_library(lib STATIC a.cc)
add_library(lib::lib ALIAS lib)
add_library(lib-static STATIC a.cc)
add_library(lib::lib-static ALIAS lib-static)
add_library(libx INTERFACE)
`,
	}
	root := tree(t, files)
	first, err := Scan(root, "lib")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		again, err := Scan(root, "lib")
		if err != nil {
			t.Fatal(err)
		}
		if again.Best != first.Best || len(again.Candidates) != len(first.Candidates) {
			t.Fatalf("scan is not deterministic on iteration %d", i)
		}
		for j := range again.Candidates {
			if again.Candidates[j].Name != first.Candidates[j].Name {
				t.Fatalf("candidate order changed on iteration %d", i)
			}
		}
	}
}

// duckdb, reduced to the shape that mattered: the only namespaced alias in the
// tree lives in a module written for the packaged release, included from a
// directory nothing adds. Reading every *.cmake made that alias outrank the
// real target with full confidence, and the name does not exist in a
// FetchContent build -- a confident wrong answer, which is the one outcome
// discovery may not produce.
func TestIgnoresTargetsFromUnreachableCMakeFiles(t *testing.T) {
	root := tree(t, map[string]string{
		"CMakeLists.txt": `
project(DuckDB)
add_subdirectory(src)
add_subdirectory(tools)
`,
		"src/CMakeLists.txt": `
add_library(duckdb SHARED ${ALL_OBJECT_FILES})
`,
		"tools/CMakeLists.txt": `
add_subdirectory(cpp)
`,
		// Reachable, and so still a candidate -- just not the best one.
		"tools/cpp/CMakeLists.txt": `
add_library(duckdb_cpp_api STATIC duckdb_cpp.cpp)
`,
		// Unreachable: only tools/cpp/package/ includes it, and no
		// add_subdirectory() names that directory.
		"tools/cpp/cmake/DuckDBCppApi.cmake": `
add_library(duckdb_cpp_api STATIC "${DUCKDB_CPP_DIR}/duckdb_cpp.cpp")
add_library(duckdb::cpp_api ALIAS duckdb_cpp_api)
`,
		"tools/cpp/package/CMakeLists.txt": `
include("${CMAKE_CURRENT_LIST_DIR}/cmake/DuckDBCppApi.cmake")
`,
	})
	res, err := Scan(root, "duckdb")
	if err != nil {
		t.Fatal(err)
	}
	if res.Best != "duckdb" {
		t.Errorf("Best = %q, want duckdb", res.Best)
	}
	// Demoted, not hidden: the walk is approximate, so the user still gets to
	// see the name and pick it.
	var alias *Candidate
	for i := range res.Candidates {
		if res.Candidates[i].Name == "duckdb::cpp_api" {
			alias = &res.Candidates[i]
		}
	}
	if alias == nil {
		t.Fatal("duckdb::cpp_api should still be listed as a candidate")
	}
	if alias.Score >= res.Candidates[0].Score {
		t.Errorf("duckdb::cpp_api scored %d, not below the winner's %d",
			alias.Score, res.Candidates[0].Score)
	}
}

// The penalty must not fire when every candidate looks unreachable, because
// that means the reachability walk failed, not that the library has no targets.
func TestUnreachablePenaltyNeedsAReachableCandidate(t *testing.T) {
	root := tree(t, map[string]string{
		// No top-level CMakeLists.txt at all: nothing to walk from.
		"cmake/targets.cmake": `
add_library(widget STATIC widget.cpp)
add_library(widget::widget ALIAS widget)
`,
	})
	res, err := Scan(root, "widget")
	if err != nil {
		t.Fatal(err)
	}
	if res.Best != "widget::widget" {
		t.Errorf("Best = %q, want widget::widget", res.Best)
	}
}
