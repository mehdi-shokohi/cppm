# cppm — Roadmap

What is planned, in what order, and why. Current state is in the
[README](../README.md#status); the reasoning behind the scope boundaries is in
[architecture.md](architecture.md).

Ordering principle: **a feature earns its place by removing CMake knowledge the user would
otherwise need.** Running a build under AddressSanitizer takes a dozen lines of fiddly CMake that
almost nobody remembers; that is exactly the kind of thing cppm should own. Features that merely
re-expose what CMake already does well are declined.

> **Two audiences, do not confuse them.** Almost everything below is about making **your C++
> project** safer — the code cppm builds for you. cppm's own Go test suite is a short, separate
> section at the end ([Maintaining cppm](#maintaining-cppm)) and is not a user-facing feature.

---

## Versioning

A tag is plain semver and names a release, not a roadmap item. `v0.2.3` is the release after
`v0.2.2`, whether it carries a roadmap item or only fixes.

Earlier tags did encode the roadmap: `v0.2.2` meant "roadmap items through 0.2.2 are in", which left
no room for a fixes-only release and forced a fourth component — `v0.2.2.1`, "`v0.2.2` plus fixes".
That scheme is retired. Four-part tags are not used again, `v0.2.2.1` stays in the history, and the
next tag is **`v0.2.3`**.

The consequence is that **`v0.2` is closed to new features**: the 0.2 line now takes fixes only, and
the safety work that was numbered 0.2.3 through 0.2.8 moved to [v0.3](#v03--making-your-c-project-safer)
as items 0.3.1 through 0.3.6. Item numbers and tag numbers are no longer the same thing, so an item
ships in whichever release it is ready for.

---

## v0.2 — Running your project's tests — **done**

cppm could build your project but could not *run* anything, which blocked every form of checking:
every safety item in v0.3 is, at bottom, "run the tests under X". Removing that blocker is what this
release was for, and both items shipped in `v0.2.2`.

| Item | Status |
|---|---|
| 0.2.1 `cppm test` | **done** — `v0.2.2` |
| 0.2.2 Dev-only dependencies | **done** — `v0.2.2` |


### 0.2.1 `cppm test` — a CTest wrapper — **done**

Prerequisite for the rest. Thin, like `cppm build`:

```sh
cppm test                 # ctest --preset debug
cppm test --release
cppm test --filter <re>   # ctest -R
cppm test --verbose
cppm test --list
cppm test --no-build      # trust the existing build tree
cppm test --fail-fast     # ctest --stop-on-failure
```

Generated projects gain `include(CTest)` in the managed include, `CMakePresets.json` gains
`testPresets` with `outputOnFailure` on, and `cppm init --with-tests` scaffolds a `tests/`
directory, a first test and the `add_test()` wiring.

What was learned building it, because it shaped the design:

- **`include(CTest)` must come after the dependencies.** It defines `BUILD_TESTING` and defaults it
  to `ON`, and a great many libraries build their own test suites when that is set. Emitting it
  before `FetchContent_MakeAvailable()` is the same failure as emitting cache options after
  `FetchContent_Declare` — constraint 7 with a different trigger. The managed include also forces
  `BUILD_TESTING` off across the dependency section (a plain variable, so dependencies'
  `option(BUILD_TESTING ...)` calls become no-ops under CMP0077) and restores it afterwards.
- **`include(CTest)`, not a bare `enable_testing()`.** The module writes the
  `DartConfiguration.tcl` that `ctest -T memcheck` reads, which 0.3.4 needs.
- **It is guarded by `PROJECT_IS_TOP_LEVEL`**, so a cppm project consumed through
  `add_subdirectory` does not switch testing on in its parent.
- **`cppm test` builds first.** A test run against a stale build tree reports on code that is no
  longer there, which is worse than no result; `--no-build` opts out.
- **`ctest` exits 8 on "No tests were found"**, which reads as a build failure. cppm counts the
  tests first and says what to do instead — and distinguishes "this project has no tests" from
  "your `--filter` matched none of them", because the remedies differ.
- **The scaffolded suite uses no framework.** `init` has to work offline and before any dependency
  exists, so the first test compiles against nothing but the standard library. CTest judges a test
  by its exit status, so that is the whole protocol.

### 0.2.2 Dev-only dependencies — **done**

Catch2, googletest, doctest and benchmark are test-only, but `cppm add` currently links every
dependency to every target. That bloats release builds with test frameworks.

```json
"dependencies": {
  "Catch2": { "source": "git", "url": "...", "version": "v3.7.1", "dev": true }
}
```

```sh
cppm add github:catchorg/Catch2@v3.7.1 --dev
```

A `dev` dependency is fetched and made available but linked only to test targets, and
`cppm_link_dependencies` skips it. This had to land with or before 0.2.1, or the test story would
have been wrong from the first release.

How it ended up working, and why:

- `CPPM_DEPENDENCIES` holds the dependencies that ship; dev ones are tracked in
  `_cppm_dev_dependencies` instead. Existing projects have no dev dependencies, so this changes
  nothing for them — which is what keeps it inside constraint 3.
- A target carries `"test": true` in `cppm.json`, and `cppm_link_dependencies()` adds the dev
  dependencies when linking one. That is the only way `cppm add --dev` can take effect at all:
  cppm does not edit `CMakeLists.txt` after `init`, so it cannot add a link line there.
- `"test": true` is **declared, never inferred**. cppm does not decide that a target called
  `foo_tests` is a test: guessing wrong either leaks a framework into a release binary or leaves a
  test unlinked.
- A dev dependency listed on a non-test target is a manifest validation error naming both possible
  edits, not a warning.
- `--target` now also applies to `local:` dependencies. It was documented for `add` but
  `addPath` never consulted it, so a local library's target name could not be pinned at all.

---

## v0.3 — Making your C++ project safer

The headline feature set, and the one with the clearest payoff. Everything here is about the code
**cppm builds for you**: catching undefined behaviour, memory errors and data races in your own C++,
with the configuration already written. It is unblocked work: `cppm test` landed in `v0.2.2`, so
every item below now has something to run.

| Item | Status |
|---|---|
| 0.3.1 Warnings and hardening | **next** |
| 0.3.2 Debug standard library | planned |
| 0.3.3 Sanitizer presets | planned |
| 0.3.4 Valgrind, Helgrind and DRD | planned |
| 0.3.5 Static analysis | planned |
| 0.3.6 CI stages | planned |

The checks stack, cheapest first — a project should adopt them in this order:

```
warnings          no runtime cost, catches the most per unit of effort
hardening         negligible cost, safe for release builds
debug STL         only works because cppm builds dependencies from source
sanitizers        5-15x slower, finds races and memory errors precisely
valgrind          20-100x slower, no recompilation, finds lock-order bugs
static analysis   no runtime at all, runs in CI
```

Each of those is both a local command and a **CI stage** (0.3.6). One declaration in `cppm.json`
drives the local preset and the generated pipeline, so a check cannot pass on your machine and fail
in CI because two files drifted apart.

The stack is also why the `checks` block in 0.3.6 is a **closed set** of names cppm knows, not a
place to plug in an arbitrary external tool. cppm's value in each row above is the gotchas it
encodes — ASan and TSan are mutually exclusive, never run an instrumented binary under valgrind,
`CMAKE_CXX_CLANG_TIDY` leaks into dependencies — and it cannot know those for a tool named at
runtime. A generic step runner would remove no CMake knowledge, which is this document's bar for
inclusion.

### 0.3.1 Warnings and hardening — the cheapest safety there is — **next**

Worth doing before sanitizers, because it costs no runtime and catches a different, larger class of
bug. Most C++ projects ship with warnings effectively off because nobody remembers the flags.

```sh
cppm build --warnings=strict      # or "safety": { "warnings": "strict" } in cppm.json
```

| Level | GCC/Clang | MSVC |
|---|---|---|
| `default` | `-Wall -Wextra` | `/W3` |
| `strict` | adds `-Wpedantic -Wshadow -Wconversion -Wold-style-cast -Wnon-virtual-dtor -Wcast-align -Wunused -Woverloaded-virtual -Wdouble-promotion -Wformat=2` | `/W4 /permissive-` |
| `error` | `strict` plus `-Werror` | `/WX` |

**Hardening** is separate and belongs in release builds, where it costs almost nothing:

```
-D_GLIBCXX_ASSERTIONS           bounds-check std containers (ABI-compatible)
-D_FORTIFY_SOURCE=2             checked libc calls  (needs -O1 or higher)
-fstack-protector-strong        stack smashing detection
-fstack-clash-protection
-fcf-protection                 control-flow integrity
-Wl,-z,relro,-z,now             linker hardening
```

Gotchas to encode rather than let users hit:

- **`-Wall -Wextra` would warn on dependencies too.** cppm already marks them `SYSTEM` (CMake 3.25+),
  which suppresses their warnings — so this works cleanly here but must fall back gracefully below
  3.25, where warnings from fmt and spdlog would otherwise drown the user's own.
- **`_FORTIFY_SOURCE` silently does nothing at `-O0`** and warns on redefinition. Set it only for
  optimised configurations.
- **`-D_GLIBCXX_DEBUG` is a separate, heavier thing** — see 0.3.2.

### 0.3.2 Debug standard library

Catches iterator invalidation, out-of-range access and container misuse that neither warnings nor
ASan reliably find.

```
libstdc++:  -D_GLIBCXX_DEBUG -D_GLIBCXX_DEBUG_PEDANTIC
libc++:     -D_LIBCPP_HARDENING_MODE=_LIBCPP_HARDENING_MODE_DEBUG
```

**`_GLIBCXX_DEBUG` changes the ABI**, so every translation unit linked together must use it —
including every dependency. With system-installed libraries that is usually impossible, which is why
most projects never turn it on.

**cppm's design makes this work:** dependencies are built from source in the same tree with the same
flags, so the whole program is consistent by construction. This is a genuine advantage of the
FetchContent model and should be documented as one.

### 0.3.3 Sanitizer presets

New configure presets, selected by `cppm build --sanitize=<name>`:

| Preset | Flags | Catches |
|---|---|---|
| `asan` | `-fsanitize=address -fno-omit-frame-pointer -g -O1` | heap/stack overflow, use-after-free, leaks |
| `ubsan` | `-fsanitize=undefined -fno-sanitize-recover=all -g -O1` | signed overflow, bad shifts, misaligned access, bad casts |
| `tsan` | `-fsanitize=thread -g -O1` | **data races, lock-order inversions** |
| `asan+ubsan` | both flag sets | the common default pairing |

Flags go in `cacheVariables` as `CMAKE_CXX_FLAGS` **and** `CMAKE_EXE_LINKER_FLAGS` — a sanitizer
compiled in but not linked produces undefined references, which is the single most common way people
get this wrong.

Constraints the tool must encode, not leave to be discovered:

- **ASan and TSan are mutually exclusive.** Requesting both must be a clear error, not a confusing
  link failure.
- **TSan needs everything instrumented.** Partially instrumented code yields false positives.
  Because dependencies are built in the same tree, cppm's flags reach them automatically — another
  point in the design's favour.
- **A dependency can overrule those flags, and one already does.** duckdb appends its own
  `-fsanitize=address -fsanitize=undefined` to `CMAKE_CXX_FLAGS_DEBUG`, because its `ENABLE_SANITIZER`
  and `ENABLE_UBSAN` both default ON. So `--sanitize=tsan` against a default duckdb puts ASan and
  TSan in one translation unit and the compiler rejects it — the exact failure the bullet above
  promises to turn into a clear error, arriving *after* cppm's own check has passed. Reaching a
  dependency is not the same as having the last word on it. This preset set is not done until
  cppm detects dependency-injected sanitizer flags and names the option to turn them off, and until
  it has been exercised against a dependency that writes `CMAKE_CXX_FLAGS_<CONFIG>` itself.
- **ASan includes LeakSanitizer on Linux but not macOS.** On macOS it needs
  `ASAN_OPTIONS=detect_leaks=1` and is unreliable; `doctor` should say so rather than silently
  differing.
- **MSVC supports only `/fsanitize=address`.** No UBSan, no TSan. Requesting them must fail with
  that explanation.
- **MemorySanitizer is deliberately omitted.** It requires every dependency *including libc++* to be
  instrumented, which in practice means building a custom standard library. Valgrind's memcheck
  covers the same ground without that cost.
- Useful defaults: `ASAN_OPTIONS=abort_on_error=1:detect_stack_use_after_return=1`,
  `UBSAN_OPTIONS=print_stacktrace=1:halt_on_error=1`, `TSAN_OPTIONS=second_deadlock_stack=1`.

### 0.3.4 Valgrind, Helgrind and DRD

A different mechanism: no recompilation, the binary runs under emulation. Wired through CTest's
built-in memcheck support rather than reinvented.

```sh
cppm test --memcheck     # leaks, invalid reads/writes, uninitialised values
cppm test --helgrind     # data races AND lock-order violations
cppm test --drd          # lighter thread checker, better with many threads
cppm test --massif       # heap profiler
cppm test --cachegrind   # cache and branch-prediction profiling
```

Generated into the managed include:

```cmake
find_program(MEMORYCHECK_COMMAND valgrind)
set(MEMORYCHECK_COMMAND_OPTIONS
    "--leak-check=full --show-leak-kinds=all --track-origins=yes --error-exitcode=1")
set(MEMORYCHECK_SUPPRESSIONS_FILE "${CMAKE_SOURCE_DIR}/valgrind.supp")
```

**Helgrind versus TSan**, since both find races and the choice is not obvious:

| | TSan | Helgrind |
|---|---|---|
| Needs recompilation | yes | **no** |
| Speed | ~5–15× slower | ~20–100× slower |
| False positives | few | more, especially with custom synchronisation |
| Lock-order inversion | partial | **yes, thorough** |
| Works on a release binary | no | **yes** |

Recommendation to bake into the docs: use TSan routinely, and Helgrind when you cannot recompile
or when you specifically suspect a lock-ordering deadlock.

Constraints to encode:

- **Never run a sanitizer-instrumented binary under valgrind.** They fight and the output is
  meaningless. cppm must refuse the combination.
- **`--error-exitcode=1` is essential**, or CI passes while valgrind reports errors.
- **A suppressions file is almost always needed** for system libraries. `cppm init` should scaffold
  a commented `valgrind.supp`, and `--gen-suppressions=all` should be offered to produce entries.
- **Valgrind is effectively Linux-only** — broken on Apple Silicon, absent on Windows. Elsewhere
  `doctor` should recommend ASan/TSan, which overlap heavily.
- Memcheck is 10–50× slower, so it belongs in a nightly job rather than on every push.

### 0.3.5 Static analysis

Finds bugs without running anything, which makes it the cheapest check in CI.

```sh
cppm build --analyze=clang-tidy    # or cppcheck
```

CMake wires these via `CMAKE_CXX_CLANG_TIDY` and `CMAKE_CXX_CPPCHECK`, and `cppm init` should
scaffold a `.clang-tidy` with a sensible check set (`bugprone-*`, `cert-*`, `clang-analyzer-*`,
`performance-*`, `modernize-*`).

**Important trap:** `CMAKE_CXX_CLANG_TIDY` is inherited by subdirectories, so setting it in the
managed include would run clang-tidy over every dependency — enormously slow and noisy, and the
findings would not be the user's to fix. It must be applied as a **per-target property on the user's
own targets only**. This is the kind of detail that makes the feature worth cppm owning rather than
leaving to a snippet copied from a blog post.

### 0.3.6 CI stages for your project

Today cppm has CI for *itself* and generates none for you. Wiring an ASan job in GitHub Actions is
the same category of work as wiring ASan in CMake: a dozen fiddly lines nobody remembers. So it
belongs here too.

**The key design decision: the CI file must be thin.**

The logic lives in cppm, and each generated workflow is a few lines that call it:

```sh
cppm check --stage sanitize-asan
```

Not in generated YAML. Three reasons, and the first is the one that matters:

1. **Local and CI behave identically.** The usual pain is a check that passes locally and fails in
   CI because the YAML drifted from the Makefile. If both run `cppm check --stage X`, they cannot
   diverge.
2. cppm does not end up maintaining YAML dialects for GitHub, GitLab, Jenkins and Azure. Each
   generator becomes a thin template over the same command.
3. `cppm check` is useful on its own — run the whole pipeline locally before pushing.

### Declaring the stages

In `cppm.json`, so that one declaration drives both the local presets and the CI file, exactly as it
already drives the CMake:

```json
"checks": {
  "warnings":   "error",
  "sanitizers": ["asan+ubsan", "tsan"],
  "valgrind":   ["memcheck", "helgrind"],
  "analyze":    ["clang-tidy"],
  "coverage":   true,
  "platforms":  ["linux", "macos", "windows"],
  "compilers":  ["gcc", "clang"]
}
```

```sh
cppm check                          # every configured stage, locally
cppm check --stage sanitize-tsan    # one stage
cppm check --list                   # what is configured
cppm ci --github                    # emit .github/workflows/cppm.yml
cppm ci --gitlab                    # emit .gitlab-ci.yml
cppm ci --script                    # emit a plain shell script, for any other CI
```

### The stage set

Ordered fast-to-slow, so failures surface early:

| Stage | Runs | Typical cost | Platforms |
|---|---|---|---|
| `warnings` | build with `-Werror` | fast | all |
| `build` | matrix build | fast | all |
| `test` | `ctest` | fast | all |
| `analyze-clang-tidy` | static analysis | medium, no runtime | linux |
| `sanitize-asan` | ASan + LeakSanitizer | 2–3× | linux, macos |
| `sanitize-ubsan` | UBSan | ~1.2× | linux, macos |
| `sanitize-tsan` | ThreadSanitizer | 5–15× | linux |
| `coverage` | gcov/llvm-cov, upload | medium | linux |
| `valgrind-memcheck` | Memcheck | 10–50× | linux, **nightly** |
| `valgrind-helgrind` | Helgrind | 20–100× | linux, **nightly** |

### Practical CI facts to encode

These are the things that make a generated pipeline actually work, and the reason this is worth
generating rather than documenting:

- **Each sanitizer needs its own build directory.** The flags differ, so reusing a build tree makes
  CMake reconfigure from scratch anyway — or worse, silently mixes instrumented and uninstrumented
  objects.
- **Cache `~/.cache/cppm` and `build/*/_deps`.** Without it every job re-clones every dependency,
  which dominates CI time. This is the single biggest speed win and the easiest thing to forget.
- **Sanitizers belong on Linux only by default.** A 3 OS × 2 compiler × 4 sanitizer matrix is 24
  jobs for no extra signal; the bugs are not platform-specific.
- **Valgrind stages must be `schedule`-triggered, not `push`.** At 10–50× slowdown they will
  otherwise make every pull request unbearable.
- **`--error-exitcode=1` for valgrind, `halt_on_error=1` for UBSan.** Without them the job goes
  green while the tool reports errors — the worst possible outcome, since it buys false confidence.
- **Windows cannot run UBSan, TSan or valgrind.** The generator must omit those jobs rather than
  emit ones that always fail.
- **macOS has no LeakSanitizer.** The ASan job there should not claim to check for leaks.
- **ccache or sccache** on the compiler, keyed per stage.

### Templates that come with it

- `cppm init --with-tests` → a `tests/` directory and a working first test
- a commented `valgrind.supp` and a `.clang-tidy`
- sanitizer environment defaults
- `cppm ci --github` → the workflow above, with caching and the platform exclusions already correct

## v0.4 — Correctness and reproducibility

### 0.4.1 Transitive dependency pinning

The largest known correctness gap. `cppm.lock` records what cppm fetches; when a dependency
resolves its own dependencies, that is neither pinned nor recorded, so the lockfile is an incomplete
record of the build.

The mechanism is CMake 3.24 dependency providers: `cmake_language(SET_DEPENDENCY_PROVIDER ...)`
installed via `CMAKE_PROJECT_TOP_LEVEL_INCLUDES` intercepts `find_package()` and
`FetchContent_MakeAvailable()` calls made *inside* dependencies and redirects them to cppm-managed
copies. Unproven in this codebase and the most likely source of surprises, so it should be spiked
before the version is scoped.

### 0.4.2 Conflict detection

Falls out of 0.4.1 almost free: the provider sees every version request, so two dependencies
disagreeing about fmt becomes detectable. Reported, with an `overrides` entry suggested — never
solved automatically, since cppm does not compare versions.

### 0.4.3 Duplicate-copy detection

Adding both fmt and spdlog yields two copies of fmt, because spdlog compiles its bundled one.
Fixing it requires `SPDLOG_FMT_EXTERNAL=ON` *and* fmt being present — coordination cppm could do
and currently does not. At minimum `doctor` should warn that a dependency is being built twice.

### 0.4.4 Target collision diagnosis

Two dependencies each defining `uninstall` or `docs` is a hard configure failure with no clean
workaround. cppm cannot fix it, but it can recognise the error and explain the cause instead of
leaving the user with a raw CMake message.

### 0.4.5 Harvest the dependency's own `option()` declarations

The generic `BUILD_TESTING` guard (constraint 7) covers far less than it appears to. Measured
against the recipe database itself: of 25 libraries with a recorded test or example switch, **3 use
`BUILD_TESTING` and 22 invent their own name** — `FMT_TEST`, `JSON_BuildTests`, `PYBIND11_TEST`,
`RE2_BUILD_TESTING`, `BUILD_UNITTESTS`. So the guard reaches roughly an eighth of the ecosystem and
the recipe database has been silently carrying the rest. The first uncurated dependency added to a
project falls straight through it.

`discover` already reads every CMake file in the tree; it just never looks at `option()`. A second
pass collecting `option(<NAME> "<doc>" <default>)`, filtered to names matching a conservative
pattern (`TEST`, `TESTS`, `TESTING`, `UNITTEST`, `EXAMPLE`, `BENCHMARK`, `DOC`, `SHELL`, `INSTALL`,
`SANITIZER`, `UBSAN`) **whose default is ON**, recovers what a recipe author writes by hand. For
duckdb it finds exactly the four that matter.

The suppressions must be **written into `cppm.json`, not applied invisibly**. That keeps the
"decline rather than guess" posture — the user sees each one and can delete it — and makes a wrong
guess show up in a diff instead of buried in generated CMake. It also raises the floor for every
uncurated library without adding a single recipe.

### 0.4.6 Say when a dependency is uncurated

`cppm add` reports the same success for a curated library and an uncurated one. fmt arrives with a
verified target and three options from its recipe; duckdb arrived with no target, no options, and
the same cheerful summary. That symmetry is what makes the first off-recipe dependency a bad
afternoon rather than a warning.

An uncurated dependency with no exported namespace should say so: that the target name was guessed
from CMake, that nothing is known about the options it needs, and that `cppm.json` will probably
need adjusting. Pair it with a `cppm recipe --export <dep>` that prints a contributable entry, which
is the only way a 26-entry database becomes a 200-entry one. Costs no schema change.

### 0.4.7 `doctor` should audit dependencies, not just tools

`doctor` checks the toolchain and the project's own settings and has no opinion about the
dependencies, which is where the risk actually sits. Four static checks, all cheap, all from the
already-cached source:

- **Assumes it is top-level** — `CMAKE_SOURCE_DIR` or `CMAKE_BINARY_DIR` where `PROJECT_*` was
  meant. This is what broke duckdb: its test tree resolved sources against the *consumer's* root.
  It generalises to output directories and install rules landing in the user's tree.
- **Exports no include path** — no `target_include_directories` on the linked target. True of
  duckdb, zlib and rapidjson.
- **Injects its own flags** — writes `CMAKE_CXX_FLAGS`, `CMAKE_CXX_FLAGS_<CONFIG>`, or calls
  `add_compile_options`. Feeds 0.3.3.
- **`option()` defaults still ON** that 0.4.5 would have caught.

### 0.4.8 Include directories through `cppm_link_dependencies`

Three of 26 recipes end with some form of "add the include directory yourself" — duckdb, zlib,
rapidjson — because a library can publish headers with a directory-scoped `include_directories()`
and leave the linked target carrying no include path. cppm can only hand over link targets, so the
user has to hand-edit `CMakeLists.txt`, the one file cppm promises never to touch.

An `includeDirs` field on the recipe and the manifest, emitted inside `cppm_link_dependencies`, is
the fix, and constraint 3 already names that function as the route for exactly this kind of
addition. The only item in this section that changes a committed file format, so it needs deciding
rather than just doing.

---

## v0.5 — Platform parity

1. **MSVC runtime library mismatch** — dependencies built `/MD` against an `/MT` consumer produce
   link errors that look unrelated to their cause. Needs `CMAKE_MSVC_RUNTIME_LIBRARY` propagated
   consistently. Entirely untested today.
2. **Windows path length** against deep `_deps/<name>-subbuild` paths.
3. **Multi-config generator verification** — Visual Studio and Xcode ignore `CMAKE_BUILD_TYPE`;
   presets now carry `configuration`, but that path has not been exercised on real hardware.
4. **Apple Silicon**, including the universal-binary question.

---

## Fix releases

Releases that ship only fixes. Under plain semver these are ordinary patch releases — see
[Versioning](#versioning) — and they never move an item's status in the tables above. `v0.2.2.1`
predates that change and is the last four-part tag.

### `v0.2.3`

Found by adding drogon to a real project. Drogon is the first dependency used whose own target is
declared untyped, and the first whose tree is mostly find-modules; between them those two facts
produced a candidate list in which every entry was wrong.

- **Discovery could not see a target declared without a type.** `add_library(<name>)` with no
  `STATIC`/`SHARED`/`INTERFACE` keyword, sources added afterwards by `target_sources()`, is a
  common modern form and the one drogon uses. The scanner's regex required a type keyword, so
  drogon's own and only target was never a candidate. `discover` now also matches the untyped form.

- **`IMPORTED` targets were treated as candidates.** A `Find<Pkg>.cmake` module creates an imported
  target to wrap one of *this* package's dependencies, which is never what a consumer links the
  package for, and frequently does not exist at all because the `find_package()` that would define
  it sits behind an off-by-default option. Drogon's `cmake_modules/` yielded ten of them, and
  `coz::coz` — from a profiler drogon only uses when `COZ_PROFILING` is on — ranked first on the
  strength of being a canonical `ns::ns` name. `discover` now vetoes imported targets outright.

  The two bugs compounded: because the untyped form hid the one real target, *no* candidate was
  reachable, and the reachability rule added in `v0.2.2.1` is skipped entirely in that case. The
  guard meant to suppress noise is what let a find-module import reach the top of the list.

- **The prompt offered its own rejected answer as the default.** `askForTarget` defaulted to
  candidate 1 on the Enter key even when discovery had set `Confident=false` and `Why` said *"the
  best candidate does not look like it belongs to this package"*. Discovery knew the list was
  wrong and the prompt still made the wrong entry the easiest thing to pick. `Result.Unrelated` now
  carries that judgement to the caller, which withholds the default and says there is none. The
  prompt also now states that a name can be typed in — it always accepted one, but nothing said so,
  which left a user facing ten wrong options with no visible way to answer correctly.

Scanned against real trees after the change: drogon resolves to `drogon` confidently with all ten
imports rejected, and duckdb is unchanged. fmt still resolves to `fmt::fmt-c` rather than
`fmt::fmt` — a confidently wrong answer, pre-existing and not caused by these fixes. fmt builds
`fmt::fmt` inside a function where the name is a parameter, so no scanner can read it; the recipe
database supplies the right name, which is why this has never been visible in use. It is a live
violation of constraint 8 and belongs with the regression corpus below.

### `v0.2.2.1`

Found by adding duckdb to a real project — the first dependency used that has no recipe. Four
independent failures, each hidden behind the previous, so only the first was visible.

- **Discovery could recommend a target that does not exist.** `duckdb::cpp_api` is the only
  namespaced alias in duckdb's tree and it is defined in `tools/cpp/cmake/DuckDBCppApi.cmake`, a
  module for the packaged release, included only from a directory nothing adds as a subdirectory.
  The scanner read every `*.cmake` without asking whether the build reaches it, and the namespaced
  and alias bonuses then floated that file to the top: `Best=duckdb::cpp_api`, `Confident=true`,
  for a name no FetchContent build defines. A confident wrong answer is the one outcome discovery
  may not produce (constraint 8), and the better a library's packaging, the more likely it was —
  a packaging module is exactly where the tidy `ns::name` alias lives.

  `discover` now walks `add_subdirectory()` and `include()` from the top-level `CMakeLists.txt` and
  demotes targets only unreachable files define. It demotes rather than drops, because the walk is
  approximate and a target wrongly judged unreachable should lose on score, not vanish; and it is
  skipped entirely when no candidate is reachable, which means the walk failed rather than that the
  library has no targets. duckdb now ranks `duckdb` first and reports `Ambiguous`, so cppm asks.
- **A duckdb recipe**, carrying the four options the library needs and a note covering the two
  things no option can fix: that its headers need adding by hand, and that `<duckdb.h>` is the C
  API while `<duckdb.hpp>` is C++.

Not fixed here, and the reason 0.4.5 through 0.4.8 exist: the generic test-suppression guard misses
duckdb's `BUILD_UNITTESTS` entirely; duckdb's test tree resolves sources against the consumer's
root, which is upstream's bug but cppm's configure failure; the include path cannot be expressed at
all; and its Debug sanitizers break any consumer not built the same way.

---

## Maintaining cppm

Separate from everything above: this is cppm's **own** Go test suite, not a user-facing feature.
Nothing here affects projects built with cppm.

| Item | Why |
|---|---|
| **Recipe smoke matrix in CI, nightly** | Recipes rot silently: a library renames a target or re-points a tag and the recipe becomes wrong. A scheduled job that adds and builds every recipe's library is the only way to notice. Scope it as *configure every recipe, build a sampled subset*: a Debug duckdb alone is ~175 targets and several minutes, so "build them all" would make this the longest job in the repo. |
| **Fuzz the spec parser** | `go test -fuzz` on `vcs.ParseSpec`. It takes untrusted input and feeds it to git, so it is the highest-value fuzz target. |
| **Discovery regression corpus** | Keep the measured library set as fixtures, and assert the confidently-wrong count stays at zero — that is the metric that matters, not the hit rate. Started: duckdb is the first recorded confidently-wrong case, kept at zero by the reachability rule in `v0.2.2.1`. drogon (untyped target, all-imported tree) was added in `v0.2.3`. **fmt is an open confidently-wrong case** — `fmt::fmt-c` instead of `fmt::fmt`, masked in use by the recipe. |
| **Golden-file tests for generated CMake** | Makes every change to generated output visible in review instead of inferred. |
| **Coverage gate** | Report now, enforce a floor once it has settled. |
| **Full `-race`** | CI currently runs `-race -short`; the network and build paths are not covered. |
| **`staticcheck` already wired** | — |

---

## Explicitly not planned

Stating these saves repeated discussion, and each is a deliberate decision rather than a gap:

| Not planned | Reason |
|---|---|
| A package registry or index | Dependencies are git URLs. A registry is a different product with a maintenance burden cppm cannot carry. |
| Version ranges, or solving | A version is an opaque git ref. This is what lets libraries that ignore semver work at all. |
| Replacing CMake | cppm generates CMake and gets out of the way. Non-trivial projects will edit `CMakeLists.txt`, by design. |
| Becoming a build system | No compiler or linker invocation of its own, ever. CMake and Ninja do that. |
| Autotools or bare-Makefile dependencies | Needs a hand-written wrapper; the complexity is not worth it. |
| Binary package distribution | Source builds only. Prebuilt binaries mean an ABI matrix, which is vcpkg's problem to solve. |
| A GUI or TUI | — |

---

## Sequencing, and what to do next

v0.3 before v0.4, for two reasons. It is what users will actually ask for — "how do I run my tests
under ASan" is a question people have today, whereas the transitive-pinning gap is invisible until
it bites. And 0.4.1 is the one item that could still invalidate architecture, so it benefits from
the project being otherwise settled before it is attempted.

Within v0.2 the order was forced: **dev-dependencies (0.2.2) had to land with or before `cppm test`
(0.2.1)**, because a test command that links Catch2 into your release binary is wrong from the first
release and becomes a breaking change to fix later. Both are done, and v0.2 is closed to features.

**Next is 0.3.1, warnings and hardening.** It is the best value per hour of work: no runtime cost,
no new concepts for the user, and it catches more real bugs than any single sanitizer. Sanitizers
and valgrind are more impressive but find a narrower class of problem more slowly.

0.3.1 is also now cheap to verify, which it was not before: `cppm test` means a warning level can
be asserted against a project that actually builds and runs, rather than only against the text of
the generated CMake.
