# cppm — Architecture

How cppm is put together, and why it is put together that way. For using cppm, see the
[README](../README.md); for working on it, [CONTRIBUTING.md](CONTRIBUTING.md).

Implementation: **Go 1.27+** · Generates: **CMake 3.24+** · Backend: **Ninja** when available

---

## 1. What cppm is

cppm is short for **C++ maker** — not "package manager". It maintains no registry and resolves
nothing from an index; dependencies are git URLs. What it makes is the project and the CMake.

**A project scaffolder, a dependency pinner, and a wrapper over CMake presets and the build.**

It is not a CMake replacement and does not claim to generate complete or final CMake. The goal is to
get a developer from nothing to a building C++ project without knowing CMake syntax. Editing
`CMakeLists.txt` by hand afterwards is an expected, supported outcome.

That boundary is what makes the design tractable. Target discovery can be an ~80%-accurate
convenience with a clear hand-off for the rest, rather than a correctness guarantee it could not
meet.

### Deliberately out of scope

| Not done | Why |
|---|---|
| Version solving or ranges | A version is an opaque git ref; see §4 |
| Transitive dependency pinning | cppm pins what cppm fetches; a dependency's own fetches are its business |
| Conflict detection | FetchContent has no solver — first declaration wins, silently |
| Non-CMake libraries | Autotools and bare Makefiles need a hand-written wrapper |
| A package registry | Dependencies are git URLs; there is no index to maintain |

---

## 2. Design rules

1. **cppm never owns the build.** It generates CMake and shells out. CMake and Ninja do the work.
2. **Generated and hand-written files never mix.** `CMakeLists.txt` is the user's after `init` and
   is never rewritten. cppm owns `.cppm/generated/cppm.cmake` and `CMakePresets.json`.
3. **A manifest setting must be one cppm can actually apply.** A setting living in a user-owned file
   could never be changed by `sync`, which would make the manifest field decorative. So each
   setting lives wherever cppm can own it:

   | Setting | Where | Why there |
   |---|---|---|
   | `cxx.standard`, `.required`, `.extensions` | generated `cppm.cmake` | an include after `project()` can still set these |
   | `toolchain.cxx`, `.c`, `.generator` | `CMakePresets.json` | `CMAKE_CXX_COMPILER` must be set **before** `project()`, so an include is too late |
   | `cmake.buildTypes`, `.exportCompileCommands` | `CMakePresets.json` | cache variables are applied at configure time |
   | dependencies | generated `cppm.cmake` | must precede the targets that link them |
   | `project()` name and version, targets, sources | `CMakeLists.txt` | `project()` can only be called there, so cppm cannot apply changes — `doctor` reports the drift instead |

4. **Prefer declining over a confident guess.** When inference is unsure, say so and name the exact
   line to edit. A wrong link target that configures successfully is the worst thing cppm can
   produce; "I don't know, add this one line" is acceptable.
5. **Every write is atomic.** Temp file in the same directory, then rename. A half-written
   `cppm.lock` is worse than none.
6. **No shell, ever.** `exec.Command` with an argv slice. See §7.
7. **Defaults are written out explicitly.** `init` spells out every setting at its default value. A
   setting nobody can see is one nobody can change.
8. **`sync` is idempotent.** Running it twice produces byte-identical output, and that is tested.

---

## 3. The problem at the centre: finding the link target

cppm's most important single operation is knowing what string to put in `target_link_libraries`.
Everything else is bookkeeping by comparison.

**CMake will not tell you.** Asking a configured project for a dependency's targets via
`BUILDSYSTEM_TARGETS` returns only the raw underlying targets — for fmt, `fmt;fmt-header-only`.
The name users are documented to link, `fmt::fmt`, is an `ALIAS`, and CMake exposes no property or
command that enumerates aliases.

It will, however, **confirm** a name you already have: `if(TARGET fmt::fmt)` works reliably.

So discovery is: generate candidates, rank them, let CMake verify. Five tiers, first hit wins:

```
Tier 1  EXPLICIT ── manifest "targets" / --target ────── always wins
Tier 2  RECIPE ──── embedded curated database ───────── known libraries, instant
Tier 3  INFERRED ── static source scan + rank + veto ── ~80%, no configure needed
Tier 4  PROBED ──── configure-time enumerate + verify ─ recovers macro-built targets
Tier 5  ASK ─────── prompt with ranked candidates ───── ambiguous or unresolved
```

### Tier 3, and the two things that make it work

Measured at 8 of 10 real libraries correct with **zero confident errors** (fmt, spdlog,
nlohmann/json, Catch2, Eigen, SDL2, cxxopts, tomlplusplus correct; googletest and abseil declined).
Two non-obvious steps are load-bearing:

**Variable resolution.** Target names are routinely assembled from variables, so a naive grep finds
nothing at all:

```cmake
# nlohmann/json — no literal "nlohmann_json::nlohmann_json" exists anywhere to find
add_library(${NLOHMANN_JSON_TARGET_NAME} INTERFACE)
add_library(${PROJECT_NAME}::${NLOHMANN_JSON_TARGET_NAME} ALIAS ${NLOHMANN_JSON_TARGET_NAME})
```

Resolving `project()` and single-valued `set()` definitions, then expanding `${VAR}`, took measured
accuracy from 60% to 80%. Three of ten libraries produced *zero* candidates without it.

**A hard veto, not a penalty.** abseil ranked `absl::abseil_test_dll` first and recommended it with
confidence — a build that would configure, link, and be wrong. Markers like `test`, `bench`, `mock`,
`example` and `dll` now disqualify a candidate outright; if nothing survives, cppm answers ASK. The
marker is ignored when it also appears in the package's own name, so a library called *googletest*
is not vetoed by its own name.

The metric to protect in CI is therefore **`WRONG == 0`**, not the hit rate.

### Tier 3's limit, and why Tiers 2 and 4 exist

The ~20% it misses build their targets inside helper macros, which no scanner can read:

```cmake
add_library(${cmake_package_name}::${name} ALIAS ${name})  # function parameters
cxx_library(gtest "${cxx_strict}" src/gtest-all.cc)        # custom macro
```

Those values exist only while CMake is running. But the **namespace** is a literal in the source
(`set(cmake_package_name GTest ...)`, `add_library(absl::${...} ALIAS ...)`), and after a configure
`BUILDSYSTEM_TARGETS` yields the raw names the macros generated. Crossing the two and probing with
`if(TARGET)` recovers the answer — verified on googletest, which Tier 3 scores at zero candidates:

```
raw targets      = gtest;gtest_main;gmock;gmock_main
verified aliases = GTest::gtest;GTest::gtest_main;GTest::gmock;GTest::gmock_main
```

### What the recipe database is for

Not a registry — a shortcut, ~25 entries. It carries two things nothing else can supply:

- **Targets built by helper macros** (`GTest::gtest_main`, `absl::base`).
- **Cache options with no discoverable signal at all.** spdlog silently compiles its own bundled
  copy of fmt unless `SPDLOG_FMT_EXTERNAL=ON`; abseil builds against a different standard than your
  project unless `ABSL_PROPAGATE_CXX_STD=ON`. No probe can find these out.

User recipes in `$CPPM_RECIPES_DIR` or `~/.config/cppm/recipes` override the embedded set, so a
wrong entry is fixable without waiting for a release.

### Tier 5 is not always a fallback

For a genuinely ambiguous library, asking is the *correct* answer. `Catch2::Catch2` versus
`Catch2::Catch2WithMain` is a decision only the user can make. Prompts are gated on stdin being a
terminal, because a prompt in CI hangs the build forever; `--non-interactive` and `--target` cover
scripted use.

---

## 4. Versions are opaque refs

**cppm never parses, orders or compares a version string.** A version is a git ref. If a library
ignores semver, that is not cppm's concern.

Resolution is pure lookup: try the ref as a tag, then a branch, then a commit; dereference annotated
tags to the commit they point at; record the full 40-character hash. Consequences, all intended:

- **No ranges.** `^11.0`, `>=3` would require knowing what a version *means*.
- **`update` re-resolves refs; it does not find newer releases.** A branch advances, a tag normally
  does not, a commit never moves.
- **A moved tag is reported, not followed.** Pure hash comparison, no version semantics — and a
  published release changing underneath you is worth knowing about.
- **Calendar and prefixed tags just work** — `20240722.0`, `release-2.30.9`, `boost-1.86.0` — because
  none of them is ever compared.

The one unavoidable choice is bare `cppm add fmt` with no ref: cppm lists the tags and asks, or uses
the default branch when non-interactive. It does not pick a "latest" for you.

---

## 5. Files and ownership

```
~/.cache/cppm/src/<host>/<owner>/<repo>/<commit12>/   global, keyed by commit

MyProject/
├── cppm.json          intent                      you      committed
├── cppm.lock          resolution                  cppm     committed
├── CMakeLists.txt     targets, project()          YOU      committed, never rewritten
├── CMakePresets.json  compiler, generator, types  cppm     committed, rewritten by sync
├── CMakeUserPresets.json  your own presets        YOU      never touched
├── src/ include/
├── .cppm/generated/cppm.cmake                     cppm     git-ignored, rewritten by sync
└── build/<preset>/                                cppm     git-ignored
```

The cache is **global and keyed by commit**: entries are immutable, never need invalidating, and ten
projects using fmt do not mean ten clones.

`sync` may rewrite `CMakePresets.json`, so it first checks the presets schema's own `vendor` field
for cppm's stamp and refuses to discard a hand-written file.

### The generated interface is a public API

Those names live inside a `CMakeLists.txt` that cppm promised never to rewrite, so cppm **cannot
migrate them later**. The surface is deliberately four names:

| Name | Guarantee |
|---|---|
| `.cppm/generated/cppm.cmake` | the include path |
| `cppm_link_dependencies(<target> [deps...])` | linking entry point |
| `CPPM_TARGETS_<dep>` | one dependency's targets, for manual wiring |
| `CPPM_DEPENDENCIES` | all dependency names |

Everything else is prefixed `_cppm_` to mark it private, and a test enforces that.

---

## 6. Package layout

```
cmd/cppm/              flag wiring only
internal/
├── cli/               one file per command; stdlib flag, no framework
├── manifest/          cppm.json: load, validate, save, deterministic output
├── lockfile/          cppm.lock: sorted packages, clean diffs
├── project/           walks up to find cppm.json, like git
├── vcs/               spec parsing, ref resolution, git CLI, source cache
├── discover/          scan, resolve variables, veto, rank, confidence gate
├── recipes/           embedded curated database (go:embed) + user overrides
├── cmakegen/          CMakeLists template, presets, the managed include
├── toolchain/         locates cmake/ninja/git/compiler; runs them
├── atomicfile/        write-temp-and-rename
└── e2e/               drives the real binary against real CMake
```

**No third-party Go dependencies.** A tool whose purpose is to relieve dependency pain builds with
`go build` and nothing else. The CLI surface is small and regular enough not to need a framework.

Key extension point:

```go
// A new dependency source is a new implementation and nothing else changes.
type Provider interface {
    Resolve(ctx context.Context, dep manifest.Dependency) (lockfile.Package, error)
    Materialize(ctx context.Context, pkg lockfile.Package) (path string, err error)
}
```

---

## 7. Security posture

cppm executes git against user-supplied URLs and refs, so these are enforced rather than hoped for:

- **No shell.** `exec.Command` takes an argv slice; nothing is ever concatenated into a command line.
- **Leading `-` rejected** in refs, URLs and paths — git would read them as flags
  (`--upload-pack=...` is remote code execution).
- **`ext::` and `fd::` transports refused** — they make git execute arbitrary commands.
- **Cache paths sanitised** so a crafted URL cannot escape the cache root via `..`.
- **`GIT_TERMINAL_PROMPT=0`** on every call; otherwise a private or mistyped repository blocks on a
  credential prompt and cppm appears to hang.
- **Values quoted when emitted into CMake**, escaping `"`, `\` and `$`.

---

## 8. Known limitations

1. **cppm does not replace CMake** and does not generate complete CMake. Non-trivial projects will
   edit `CMakeLists.txt`; cppm stays out of the way when they do.
2. **No version solving.** Conflicts are not detected; FetchContent's first declaration wins.
3. **The lockfile covers only direct dependencies.** A dependency's own fetches are neither pinned
   nor recorded, so `cppm.lock` is not a complete record of the build.
4. **`add_subdirectory` means dependency code is part of your build.** Dependencies can set cache
   variables, mutate flags, and collide on target names. `SYSTEM` and `EXCLUDE_FROM_ALL` mitigate
   some of it; target collisions have no clean workaround.
5. **Inferred target names can be wrong** (~80% accurate). cppm declines rather than guessing when
   unsure, and labels an inferred name as inferred.
6. **Shallow fetch depends on server support**, with automatic fallback to full history.
7. **Windows and macOS are built and tested in CI but less exercised** than Linux. MSVC runtime
   library mismatches across dependencies are not yet handled.
