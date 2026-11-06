# cppm — Roadmap

What is planned, in what order, and why. Current state is in the
[README](../README.md#status); the reasoning behind the scope boundaries is in
[architecture.md](architecture.md).

Ordering principle: **a feature earns its place by removing CMake knowledge the user would
otherwise need.** Running a build under AddressSanitizer takes a dozen lines of fiddly CMake that
almost nobody remembers; that is exactly the kind of thing cppm should own. Features that merely
re-expose what CMake already does well are declined.

---

## v0.2 — Testing and diagnostics

The theme: cppm can build your project but cannot yet *run* anything, which blocks every form of
checking. Everything below depends on the first item.

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

### 0.2.3 Sanitizer presets

New configure presets, selected by `cppm build --sanitize=<name>`:

| Preset | Flags | Catches |
|---|---|---|
| `asan` | `-fsanitize=address -fno-omit-frame-pointer -g -O1` | heap/stack overflows, use-after-free, leaks |
| `ubsan` | `-fsanitize=undefined -fno-sanitize-recover=all -g -O1` | signed overflow, bad shifts, misaligned access, bad casts |
| `tsan` | `-fsanitize=thread -g -O1` | data races, lock-order inversions |
| `asan+ubsan` | both flag sets | the common default pairing |

Flags go in `cacheVariables` as `CMAKE_CXX_FLAGS` **and** `CMAKE_EXE_LINKER_FLAGS` — a sanitizer
that is compiled in but not linked produces undefined references, which is the single most common
way people get this wrong.

Constraints that must be encoded in the tool, not left to the user to discover:

- **ASan and TSan are mutually exclusive.** Requesting both must be a clear error, not a confusing
  link failure.
- **TSan needs everything instrumented.** Partially instrumented code yields false positives.
  Because dependencies are built in the same tree, cppm's flags reach them automatically — this is
  a point in the design's favour and should be stated.
- **ASan includes LeakSanitizer on Linux but not macOS.** On macOS, leak checking needs
  `ASAN_OPTIONS=detect_leaks=1` and is unreliable; `doctor` should say so rather than silently
  differing.
- **MSVC supports only `/fsanitize=address`.** No UBSan, no TSan. Requesting them on MSVC must fail
  with that explanation.
- **MemorySanitizer is deliberately omitted.** It requires every dependency *including libc++* to be
  instrumented, which in practice means building a custom standard library. Out of scope; `doctor`
  should point at valgrind instead.
- `ASAN_OPTIONS` / `UBSAN_OPTIONS` defaults worth setting: `abort_on_error=1`,
  `print_stacktrace=1`, and `detect_stack_use_after_return=1`.

### 0.2.4 Valgrind, Helgrind and friends

A different mechanism from sanitizers: no recompilation, the binary runs under emulation. Wired
through CTest's built-in memcheck support rather than reinvented.

```sh
cppm test --memcheck     # ctest -T memcheck     (Memcheck: leaks, invalid reads/writes)
cppm test --helgrind     # ctest -T memcheck     (Helgrind: races, lock-order violations)
cppm test --drd          # DRD: lighter-weight thread checker
cppm test --massif       # heap profiler, writes a report
```

Generated into the managed include:

```cmake
find_program(MEMORYCHECK_COMMAND valgrind)
set(MEMORYCHECK_COMMAND_OPTIONS
    "--leak-check=full --show-leak-kinds=all --track-origins=yes --error-exitcode=1")
set(MEMORYCHECK_SUPPRESSIONS_FILE "${CMAKE_SOURCE_DIR}/valgrind.supp")
```

Constraints to encode:

- **Never run a sanitizer-instrumented binary under valgrind.** They fight, and the output is
  garbage. cppm must refuse the combination rather than produce a confusing report.
- **`--error-exitcode=1`** is essential, or CI passes while valgrind reports errors.
- **A suppressions file is almost always needed** for system libraries. `cppm init` should scaffold
  an empty, commented `valgrind.supp`, and `--gen-suppressions=all` should be available to produce
  entries.
- **Valgrind is effectively Linux-only.** Broken on Apple Silicon, absent on Windows. On those
  platforms `doctor` should recommend ASan, which overlaps heavily in what it catches.
- Memcheck is roughly 10–50× slower than native, so this belongs in a nightly CI job rather than
  on every push.

### 0.2.5 Sanitizer and memcheck templates

Because the point is that the user should not have to assemble this themselves:

- `cppm init --with-tests` → `tests/` plus a working first test
- a commented `valgrind.supp`
- `.sanitizerrc`-style defaults for `ASAN_OPTIONS` / `UBSAN_OPTIONS`
- `cppm ci --github` → a ready workflow for the *user's* project: build and test on three platforms,
  an `asan+ubsan` job, and a nightly valgrind job

---

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

## Ongoing — cppm's own pipeline

Separate from the features above; this is about trusting the tool.

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
