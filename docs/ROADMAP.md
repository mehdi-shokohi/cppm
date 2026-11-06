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

## v0.2 — Making your C++ project safer

The headline feature set, and the one with the clearest payoff. Everything here is about the code
**cppm builds for you**: catching undefined behaviour, memory errors and data races in your own C++,
with the configuration already written.

The theme: cppm can build your project but cannot yet *run* anything, which blocks every form of
checking. Everything below depends on the first item.

The checks stack, cheapest first — a project should adopt them in this order:

```
warnings          no runtime cost, catches the most per unit of effort
hardening         negligible cost, safe for release builds
debug STL         only works because cppm builds dependencies from source
sanitizers        5-15x slower, finds races and memory errors precisely
valgrind          20-100x slower, no recompilation, finds lock-order bugs
static analysis   no runtime at all, runs in CI
```

Each of those is both a local command and a **CI stage** (0.2.8). One declaration in `cppm.json`
drives the local preset and the generated pipeline, so a check cannot pass on your machine and fail
in CI because two files drifted apart.

### 0.2.1 `cppm test` — a CTest wrapper

Prerequisite for the rest. Thin, like `cppm build`:

```sh
cppm test                 # ctest --preset debug
cppm test --release
cppm test --filter <re>   # ctest -R
cppm test --verbose       # ctest --output-on-failure
cppm test --list
```

Generated projects gain `include(CTest)` and `enable_testing()` in the managed include, and
`CMakePresets.json` gains `testPresets`. `cppm init --with-tests` scaffolds a `tests/` directory
and a first test file.

### 0.2.2 Dev-only dependencies — *a real gap today*

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
`cppm_link_dependencies` skips it. This has to land with or before 0.2.1, or the test story is
wrong from the start.

### 0.2.3 Warnings and hardening — the cheapest safety there is

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
- **`-D_GLIBCXX_DEBUG` is a separate, heavier thing** — see 0.2.4.

### 0.2.4 Debug standard library

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

### 0.2.5 Sanitizer presets

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

### 0.2.6 Valgrind, Helgrind and DRD

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

### 0.2.7 Static analysis

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

### 0.2.8 CI stages for your project

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

## v0.3 — Correctness and reproducibility

### 0.3.1 Transitive dependency pinning

The largest known correctness gap. `cppm.lock` records what cppm fetches; when a dependency
resolves its own dependencies, that is neither pinned nor recorded, so the lockfile is an incomplete
record of the build.

The mechanism is CMake 3.24 dependency providers: `cmake_language(SET_DEPENDENCY_PROVIDER ...)`
installed via `CMAKE_PROJECT_TOP_LEVEL_INCLUDES` intercepts `find_package()` and
`FetchContent_MakeAvailable()` calls made *inside* dependencies and redirects them to cppm-managed
copies. Unproven in this codebase and the most likely source of surprises, so it should be spiked
before the version is scoped.

### 0.3.2 Conflict detection

Falls out of 0.3.1 almost free: the provider sees every version request, so two dependencies
disagreeing about fmt becomes detectable. Reported, with an `overrides` entry suggested — never
solved automatically, since cppm does not compare versions.

### 0.3.3 Duplicate-copy detection

Adding both fmt and spdlog yields two copies of fmt, because spdlog compiles its bundled one.
Fixing it requires `SPDLOG_FMT_EXTERNAL=ON` *and* fmt being present — coordination cppm could do
and currently does not. At minimum `doctor` should warn that a dependency is being built twice.

### 0.3.4 Target collision diagnosis

Two dependencies each defining `uninstall` or `docs` is a hard configure failure with no clean
workaround. cppm cannot fix it, but it can recognise the error and explain the cause instead of
leaving the user with a raw CMake message.

---

## v0.4 — Platform parity

1. **MSVC runtime library mismatch** — dependencies built `/MD` against an `/MT` consumer produce
   link errors that look unrelated to their cause. Needs `CMAKE_MSVC_RUNTIME_LIBRARY` propagated
   consistently. Entirely untested today.
2. **Windows path length** against deep `_deps/<name>-subbuild` paths.
3. **Multi-config generator verification** — Visual Studio and Xcode ignore `CMAKE_BUILD_TYPE`;
   presets now carry `configuration`, but that path has not been exercised on real hardware.
4. **Apple Silicon**, including the universal-binary question.

---

## Maintaining cppm

Separate from everything above: this is cppm's **own** Go test suite, not a user-facing feature.
Nothing here affects projects built with cppm.

| Item | Why |
|---|---|
| **Recipe smoke matrix in CI, nightly** | Recipes rot silently: a library renames a target or re-points a tag and the recipe becomes wrong. A scheduled job that adds and builds every recipe's library is the only way to notice. |
| **Fuzz the spec parser** | `go test -fuzz` on `vcs.ParseSpec`. It takes untrusted input and feeds it to git, so it is the highest-value fuzz target. |
| **Discovery regression corpus** | Keep the measured library set as fixtures, and assert the confidently-wrong count stays at zero — that is the metric that matters, not the hit rate. |
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

0.2 before 0.3, for two reasons. It is what users will actually ask for — "how do I run my tests
under ASan" is a question people have today, whereas the transitive-pinning gap is invisible until
it bites. And 0.3.1 is the one item that could still invalidate architecture, so it benefits from
the project being otherwise settled before it is attempted.

Within 0.2, the order is forced: **dev-dependencies (0.2.2) must land with or before `cppm test`
(0.2.1)**, because a test command that links Catch2 into your release binary is wrong from the first
release and becomes a breaking change to fix later.

After that, 0.2.3 (warnings and hardening) is the best value per hour of work: no runtime cost, no
new concepts for the user, and it catches more real bugs than any single sanitizer. Sanitizers and
valgrind are more impressive but find a narrower class of problem more slowly.
