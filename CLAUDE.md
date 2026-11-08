# Working on cppm

Guidance for AI assistants contributing to this repository. Humans should read
[docs/CONTRIBUTING.md](docs/CONTRIBUTING.md), which covers the same ground in more detail.

## What this project is

cppm is short for **C++ maker**, not "package manager". It scaffolds a C++ project, pins git
dependencies to commits, generates the CMake wiring, and wraps `cmake --preset`. It maintains no
registry and it is **not** a CMake replacement.

Read [docs/architecture.md](docs/architecture.md) §1–§3 before changing anything non-trivial. Most
mistakes come from not knowing §3, the target-discovery problem.

## Commands

```bash
go build -o cppm ./cmd/cppm
go test ./...                  # full suite, ~80s, needs network + CMake + a compiler
go test -short ./...           # fast: skips network and compiler-heavy tests
go test -race -short ./...     # what CI runs for races
gofmt -w . && go vet ./...     # required before any commit
```

`CPPM_SKIP_NETWORK_TESTS=1` skips tests that reach GitHub. `CPPM_CACHE_DIR` relocates the source
cache; tests set it to a temp dir.

## Hard constraints

These are not style preferences. Breaking one is a bug even if the tests pass.

1. **Never write to `CMakeLists.txt` after `init`.** It is the user's file. cppm owns
   `.cppm/generated/cppm.cmake` and `CMakePresets.json`, nothing else.
2. **Never add a third-party Go dependency.** `go.sum` is empty and CI fails if it is not. A tool
   that exists to relieve dependency pain carries none itself.
3. **Never change the generated CMake public interface** — `cppm_link_dependencies`,
   `CPPM_TARGETS_<dep>`, `CPPM_DEPENDENCIES`, or the include path. These live inside a user-owned
   file cppm promised not to rewrite, so they can never be migrated. New variables get a `_cppm_`
   prefix. If a new feature seems to need a new public name, route it through
   `cppm_link_dependencies` instead — that is how dev dependencies work.
4. **Never add version parsing, comparison or ranges.** A version is an opaque git ref. This is
   declined scope, not a missing feature: it is what lets libraries that ignore semver work.
5. **Never use a shell.** `exec.Command` with an argv slice. Reject arguments beginning with `-`
   rather than escaping them — git reads `--upload-pack=...` as remote code execution.
6. **Never let `sync` move a pin.** Only `update` re-resolves. Getting this wrong destroys
   reproducibility silently.
7. **Emit cache options before `FetchContent_Declare`.** They are inert afterwards, and the result
   is every dependency building its own test suite. `BUILD_TESTING` is the same hazard by another
   route: `include(CTest)` defaults it to `ON`, so it is emitted *after* the dependencies and forced
   off across them.
8. **Prefer declining to guessing.** A confidently wrong link target produces a build that
   configures, links, and is silently wrong. In discovery, `WRONG == 0` matters more than the hit
   rate.
9. **Never put a warning or hardening flag at directory scope.** `add_compile_options()` in the
   generated include reaches every directory added below it, which is every `FetchContent`
   dependency -- so `-Werror` lands on fmt and spdlog, where a warning is not the user's bug and an
   upstream release can stop their build. They go on the project's own targets, `PRIVATE`, through
   the deferred `_cppm_apply_safety`. Sanitizer flags are the deliberate exception: they must
   instrument the whole program, so they go in `CMAKE_CXX_FLAGS` and the linker flags *before* the
   dependencies. Getting it backwards gives a sanitizer run that finds nothing, or a warning level
   that an upstream release can break. See docs/architecture.md section 5.
10. **A sanitizer preset carries a name, not flags, and no `CMAKE_BUILD_TYPE`.** The preset sets
   `_cppm_sanitize`; the generated include chooses the flags, because only CMake knows whether the
   compiler is MSVC (which has `/fsanitize=address` and nothing else). A build type's `-O0`/`-O2`
   would override the `-O1` the sanitizer flags ask for, and would switch `_FORTIFY_SOURCE` on,
   which hides the overflows ASan exists to find.
11. **`MEMORYCHECK_*` goes before `include(CTest)`, and never through `find_program` directly.**
   The CTest module reads those variables to write `DartConfiguration.tcl`; setting them later is
   inert. And a `find_program` writing straight into `MEMORYCHECK_COMMAND` leaves it `NOTFOUND`
   for the CTest module's own search to fill with NVIDIA's `compute-sanitizer`.
12. **`ctest -T memcheck` needs `--test-dir`, and a valgrind run must prove it ran.** `-T memcheck`
   is a dashboard action and reads `DartConfiguration.tcl` from the *current* directory, not the
   preset's binary dir; without `--test-dir` it reports "No tests were found!!!" and **exits 0**,
   so the run looks like a clean pass having checked nothing. Every safety feature needs an e2e
   test that uses the real tool: this one had passing unit tests, correct generated CMake, and did
   nothing.

13. **A sanitizer launched outside CTest must be given its runtime options.** The halting options
   (`ASAN_OPTIONS=abort_on_error=1` and the rest) live in the *test* preset's `environment` in
   `CMakePresets.json`, and nothing applies them to a binary cppm executes itself. Without them a
   sanitizer prints its finding and **exits 0** — the same silent pass as constraint 12, by another
   route. `cppm run` sets them from `safety.Sanitizer.Env`; anything else that launches an
   instrumented binary must too. A user-exported value is appended *after* cppm's, never instead of
   it: these lists are read left to right and the last setting wins.

If a task seems to require breaking one of these, say so and stop rather than working around it.
The constraint is usually the point.

## Conventions

- **Comments explain *why*, never *what*.** Record the constraint that is not visible from the
  code — that `CMAKE_CXX_COMPILER` must be set before `project()`, or that a soft scoring penalty
  was tried and was not enough. Never `i++ // increment i`.
- **Errors say what to do.** Name the file and line to change. cppm's users do not know CMake, so
  an error only a CMake expert can act on has failed.
- **Atomic writes** via `internal/atomicfile` for anything persistent.
- **Deterministic serialisation.** Manifests and lockfiles are committed; unstable key order
  produces noisy diffs.
- **Add a test that fails before the fix.** Especially for anything in `internal/discover` or
  `internal/vcs`.

## Where things live

| Package | Responsibility |
|---|---|
| `internal/cli` | one file per command; stdlib `flag`, no framework |
| `internal/manifest` | `cppm.json` |
| `internal/lockfile` | `cppm.lock` |
| `internal/vcs` | spec parsing, ref resolution, git CLI, source cache |
| `internal/discover` | scan, resolve variables, veto, rank, confidence gate |
| `internal/recipes` | embedded curated database + user overrides |
| `internal/sources` | expands `sourceGlobs` patterns; no cppm imports, so `manifest` can use it |
| `internal/safety` | warning, hardening and sanitizer flag tables; valgrind options; dependency-flag scanning. No cppm imports either |
| `internal/cmakegen` | the CMakeLists template and the managed include |
| `internal/toolchain` | locates and runs cmake/ctest/ninja/git/compiler; reports how a launched program ended |
| `internal/e2e` | drives the real binary against real CMake |

## Commit messages

Explain **why**, not what — the diff shows what. If you discovered something non-obvious, a CMake
constraint or a wrong assumption, put it in the message; that is where the next person will look.
Do not pad with bullet summaries of each changed file.
