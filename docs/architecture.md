# cppm — Preliminary Architecture

Companion to [evaluation.md](evaluation.md). Revises §4, §6, §7, §8 and §11 of
[proposal.md](../proposal.md) for a Go implementation.

Implementation language: **Go 1.27+** · Generated build system: **CMake 3.24+** · Backend: **Ninja**

---

## 0. What cppm is

**A project scaffolder, a dependency pinner, and a wrapper over CMake presets and the build.**

It is *not* a CMake replacement and does not claim to generate complete or final CMake. The goal is
to get a developer from nothing to a building C++ project without knowing CMake syntax. Editing
`CMakeLists.txt` by hand afterwards is an expected and supported outcome.

This boundary is what makes the design tractable: target inference can be an 80%-accurate
convenience with a clear failure path (see
[evaluation.md](evaluation.md) §2.5) rather than a correctness guarantee it could not meet.

---

## 1. Design rules

1. **cppm never owns the build.** It generates CMake and shells out. CMake and Ninja do the work.
2. **Generated and hand-written files never mix.** Everything cppm writes lives under
   `.cppm/generated/` and carries a "do not edit" banner. `CMakeLists.txt` is the user's file after
   `init` and is never rewritten.
2b. **Prefer ASK over a confident guess.** When inference is not confident, say so and name the
   exact line to edit. A wrong link target that configures successfully is the worst outcome the
   tool can produce; "I don't know, add this one line" is an acceptable one.
3. **The lockfile is the source of truth for builds.** The manifest states intent; the lock states
   what will actually be fetched. `sync` honours the lock; only `update` moves it.
4. **Every write is atomic.** Temp file in the same directory, then rename.
5. **No shell, ever.** `exec.Command` with an argv slice, `--` terminators, validated inputs.
6. **Idempotence is testable.** `sync` twice must produce byte-identical output. Make this a test.

---

## 2. Manifest schema — `cppm.json`

Changes from the proposal are marked.

```json
{
  "schema": 1,
  "name": "MyProject",
  "version": "0.1.0",

  "cxx": { "standard": 20, "required": true },
  "cmake": { "minimum": "3.24" },

  "targets": {
    "MyProject": {
      "kind": "executable",
      "sources": ["src/main.cpp"],
      "dependencies": ["fmt", "json"]
    }
  },

  "dependencies": {
    "fmt": {
      "source": "git",
      "url": "https://github.com/fmtlib/fmt.git",
      "version": "11.1.4",
      "targets": ["fmt::fmt"],
      "options": { "FMT_TEST": "OFF", "FMT_DOC": "OFF" }
    },
    "json": {
      "source": "git",
      "url": "https://github.com/nlohmann/json.git",
      "version": "3.11.3"
    },
    "MyLibrary": {
      "source": "path",
      "path": "../MyLibrary"
    }
  },

  "overrides": {}
}
```

**Deviations from the proposal, and why**

| Change | Reason |
|---|---|
| `"source": "git"` + explicit `url`, replacing `"source": "github"` + `"repository"` | `github:` is CLI sugar, not a schema concept. Keeping the schema host-agnostic means GitLab, Codeberg and self-hosted work with no schema change. The CLI expands `github:fmtlib/fmt` into the full URL at parse time. |
| `options` per dependency | Required, not optional — [evaluation.md](evaluation.md) §3.2. Without it the second dependency you add builds its own test suite. |
| `targets` per dependency | Tier-1 explicit override for discovery, which must exist from day one. Omitted means "infer". |
| `targets` top-level section | Supports multiple executables/libraries and per-target dependency sets, replacing the blunt `cppm_link_dependencies(X)` — §3.5. |
| `overrides` | Declared conflict resolution — §3.6. Empty in the common case. |
| `"schema": 1` | Lets the manifest be migrated later without guesswork. |
| `cxx` as an object | `"language": "cxx20"` is a string that must be parsed; a standard and a flag are two facts. |

`source` is an open enum: `git`, `path`, and later `archive` / `find_package`.

---

## 3. Lockfile schema — `cppm.lock`

```json
{
  "schema": 1,
  "generated_by": "cppm 0.1.0",
  "packages": [
    {
      "name": "fmt",
      "source": "git",
      "url": "https://github.com/fmtlib/fmt.git",
      "requested": "11.1.4",
      "resolved_ref": "refs/tags/11.1.4",
      "commit": "123913715afeb8a437e6388b4473fcc4753e1c9a",
      "targets": ["fmt::fmt"],
      "target_origin": "inferred",
      "options": { "FMT_TEST": "OFF", "FMT_DOC": "OFF" },
      "dependencies": [],
      "shallow_ok": true
    }
  ]
}
```

- `commit` is always a full 40-character hash. A tag is never sufficient — tags move.
- `target_origin` is one of `explicit` / `recipe` / `inferred`, so `cppm info` can tell the user
  *why* a target name was chosen and `doctor` can flag guesses.
- `shallow_ok` records whether shallow fetch succeeded for this host, avoiding a repeat probe of a
  server that rejects it — [evaluation.md](evaluation.md) §2.3.
- `packages` is an **array sorted by name**, not an object. Deterministic serialisation means clean
  Git diffs.
- Local `path` dependencies have no commit and are never locked to a hash — they are editable by
  design.

---

## 4. Target discovery

The core mechanism. Three tiers, first hit wins.

```
┌─ Tier 1  EXPLICIT ── manifest "targets" ──────────────────── always wins
├─ Tier 2  RECIPE ──── embedded curated DB (go:embed) ──────── known libraries, instant
├─ Tier 3  INFERRED ── static source scan + rank + veto ────── 80%, no configure needed
├─ Tier 4  PROBED ──── configure-time enumerate + verify ───── recovers macro-based projects
└─ Tier 5  ASK ─────── prompt with ranked candidates ───────── ambiguous or unresolved
```

Tiers 1–3 are fast and cover the overwhelming majority. Tier 4 costs one CMake configure. Tier 5
involves the user, which for ambiguous packages is the right answer rather than a failure.

**Tier 3 pipeline** — measured at **80% correct, 0 confident errors** across ten real libraries
([evaluation.md](evaluation.md) §2.5). Prototype: [tools/phase0](../tools/phase0/main.go).

1. **Clone** into the global mirror cache at the locked commit.
2. **Resolve variables.** Collect the top-level `project()` name and single-valued `set()`
   definitions, shallowest file first, then expand `${VAR}` with a bounded number of passes.
   *Not optional* — target names are routinely assembled from variables, and adding this step alone
   moved the measurement from 60% to 80%.
3. **Scan** `CMakeLists.txt` and `*.cmake` for `add_library(<name> ALIAS <real>)` and
   `add_library(<name> INTERFACE|STATIC|SHARED|MODULE|OBJECT)`, skipping `test/`, `example/`,
   `benchmark/`, `third_party/`, `extern/`, `vendor/` and `doc/` subtrees. This recovers the
   namespaced names CMake itself cannot enumerate (§2.1). Names still containing `$` after
   expansion are discarded as unusable.
4. **Veto.** Disqualify outright — not merely demote — any candidate whose bare name contains
   `test`, `bench`, `mock`, `example`, `sample`, `fuzz`, `dll` or `buildall`. The marker is ignored
   when it also appears in the package's own name, so *googletest* is not vetoed by its own name.
   This exists because a soft penalty was insufficient: abseil ranked `absl::abseil_test_dll` first
   and recommended it with confidence.
5. **Rank** the survivors: `+50` namespaced, `+20` alias, `+40`/`+20` bare name matching the package,
   `+25` namespace matching the package, `+15` for the canonical `ns::ns` form, `+5` interface,
   `-30` for `header_only`/`static`/`shared`/`main`, minus a small length penalty. Package identity
   is matched against **both** the dependency name and the dependency's own `project()` name, which
   is what makes `nlohmann_json::nlohmann_json` reachable from a dependency called `json`.
6. **Gate on confidence.** Answer only if the top candidate survived the veto *and* is plausibly
   related to the package. Otherwise report ASK — no guess. Protect `WRONG == 0` in CI; it matters
   more than the hit rate.
7. **Probe** the winner at configure time with `if(TARGET x)`. Reliable — verified with no false
   positives across seven candidate names.
8. **Persist** to the lock as `target_origin: inferred` and warn once, so the user knows a guess was
   made and can pin it.

**Tier 3's limit.** The 20% it misses declare targets through helper macros rather than literal
`add_library` calls, so no scanner can read them:

```cmake
add_library(${cmake_package_name}::${name} ALIAS ${name})  # parameters, not variables
cxx_library(gtest "${cxx_strict}" src/gtest-all.cc)        # custom macro
```

These fall through to Tier 4.

---

### Tier 4 — configure-time probe (fallback)

Verified on googletest, which Tier 3 scores at zero candidates
([evaluation.md](evaluation.md) §2.6).

Macro-based projects hide the *bare* name but leak the **namespace** as a literal
(`set(cmake_package_name GTest ...)`, `add_library(absl::${...} ALIAS ...)`). After a configure,
`BUILDSYSTEM_TARGETS` yields the raw names the macros generated. Combining them resolves the case:

1. Harvest namespace candidates statically, plus the `project()` name.
2. Configure once, then read `BUILDSYSTEM_TARGETS` for each dependency subdirectory.
3. Generate the cross product `<ns>::<raw>`, plus a `<ns>_`-prefix-stripped variant
   (`absl_base` → `absl::base`).
4. Verify each with `if(TARGET)` — the one question CMake answers reliably.
5. Write the winners to the lock so the cost is paid once.

```
SPIKE_RAW              = gtest;gtest_main;gmock;gmock_main
SPIKE_VERIFIED_ALIASES = GTest::gtest;GTest::gtest_main;GTest::gmock;GTest::gmock_main
```

Costs one configure, which `build` performs anyway. So `add` stays fast via Tiers 1–3 and Tier 4
runs only for the hard minority.

---

### Tier 5 — ask the user

When Tier 4 leaves more than one plausible answer, or none, `cppm add` prompts with the ranked
candidates. For genuinely ambiguous packages this is the *correct* resolution rather than a
fallback: `Catch2::Catch2` vs `Catch2::Catch2WithMain` is a user choice, not a discoverable fact.

```
$ cppm add github:catchorg/Catch2@v3.7.1
  Catch2 exposes more than one linkable target:
    1) Catch2::Catch2WithMain   test library + main()      (recommended)
    2) Catch2::Catch2           test library, you supply main()
    3) enter a target name manually
    4) skip — I will wire it myself in CMakeLists.txt
  choice [1]:
```

Rules, because this must never hang a build:

- Prompt **only** when stdin is a TTY; otherwise record `target_origin: unresolved`, warn, and let
  the configure-time `FATAL_ERROR` deliver the message.
- `--non-interactive` forces that path explicitly, for CI.
- `--target <name>` supplies the answer up front, so scripted use never needs the prompt.
- Option 4 is always available and is a supported end state — §0.

**Recipe entry format** (embedded, also user-extensible via `~/.config/cppm/recipes/`):

```json
{
  "url": "https://github.com/gabime/spdlog.git",
  "targets": ["spdlog::spdlog"],
  "options": { "SPDLOG_BUILD_EXAMPLE": "OFF", "SPDLOG_FMT_EXTERNAL": "ON" },
  "requires": ["fmt"],
  "notes": "SPDLOG_FMT_EXTERNAL avoids a second bundled copy of fmt"
}
```

That `SPDLOG_FMT_EXTERNAL` entry is exactly the per-package knowledge that §2.4 showed cannot be
inferred.

---

## 5. Generated CMake

### `.cppm/generated/cppm_dependencies.cmake`

```cmake
# Generated by cppm 0.1.0 — DO NOT EDIT.
# Regenerate with: cppm sync
include_guard(GLOBAL)
include(FetchContent)

set(CPPM_DEPENDENCIES "")

# ---- fmt @ 11.1.4 ----
set(FMT_TEST OFF CACHE BOOL "" FORCE)
set(FMT_DOC  OFF CACHE BOOL "" FORCE)
FetchContent_Declare(fmt
  GIT_REPOSITORY "https://github.com/fmtlib/fmt.git"
  GIT_TAG        123913715afeb8a437e6388b4473fcc4753e1c9a
  GIT_SHALLOW    ON
  EXCLUDE_FROM_ALL
)
set(CPPM_TARGETS_fmt "fmt::fmt")
list(APPEND CPPM_DEPENDENCIES fmt)

FetchContent_MakeAvailable(fmt)

# ---- verification ----
foreach(_dep IN LISTS CPPM_DEPENDENCIES)
  foreach(_t IN LISTS CPPM_TARGETS_${_dep})
    if(NOT TARGET ${_t})
      message(FATAL_ERROR
        "cppm: dependency '${_dep}' does not provide target '${_t}'.\n"
        "cppm could not determine the right target name. Either pin it in cppm.json:\n"
        "    \"${_dep}\": { ..., \"targets\": [\"<real::target>\"] }\n"
        "or link it yourself in CMakeLists.txt — cppm will not overwrite your edits.\n"
        "Run 'cppm info ${_dep}' to see the candidates that were considered.")
    endif()
  endforeach()
endforeach()

function(cppm_link_dependencies target)
  set(_deps ${ARGN})
  if(NOT _deps)
    set(_deps ${CPPM_DEPENDENCIES})
  endif()
  foreach(_dep IN LISTS _deps)
    target_link_libraries(${target} PRIVATE ${CPPM_TARGETS_${_dep}})
  endforeach()
endfunction()
```

Three things the proposal's sketch lacks:

- **`EXCLUDE_FROM_ALL`** keeps dependency install rules and stray targets out of your build.
- **Options set before `FetchContent_Declare`**, which is the only point at which they take effect.
- **A verification loop** that fails configure with an actionable message naming the exact JSON to
  edit. Given that discovery is heuristic, the failure path is a primary user experience, not an
  edge case.

`cppm_link_dependencies` accepts an explicit dependency list, addressing §3.5, while bare
`cppm_link_dependencies(App)` still links everything for the simple case.

### Dependency provider — `.cppm/generated/cppm_provider.cmake`

Injected via `CMAKE_PROJECT_TOP_LEVEL_INCLUDES` so that `find_package(fmt)` calls made *inside*
dependencies are redirected to the cppm-managed copy rather than the system. This is the CMake 3.24
mechanism from §3.3 and the only clean answer to nested dependency resolution. Phase 3.

---

## 6. Go package layout

```
cppm/
├── go.mod
├── cmd/cppm/main.go                 thin — flag wiring only
├── internal/
│   ├── cli/                         cobra commands, one file each
│   │   ├── root.go  init.go  add.go  remove.go  sync.go
│   │   └── update.go  build.go  list.go  info.go  doctor.go  clean.go
│   ├── manifest/                    cppm.json: load, validate, save, migrate
│   ├── lockfile/                    cppm.lock: load, save, deterministic order
│   ├── resolve/                     constraint → ref → commit; tag normalisation (§3.8)
│   ├── provider/                    Provider interface; git.go, path.go
│   ├── vcs/                         git CLI wrapper, mirror cache, shallow fallback
│   ├── discover/                    source scan, candidate ranking, probe (§4)
│   ├── recipes/                     embedded recipe DB + user overrides
│   ├── cmakegen/                    dependency .cmake, presets, CMakeLists template
│   ├── buildrunner/                 cmake --preset orchestration
│   ├── doctor/                      toolchain and project diagnostics
│   └── atomic/                      write-temp-and-rename
└── testdata/script/                 testscript CLI integration tests
```

Mapping from the proposal's §11 C++ layout is one-to-one, with `discover/` and `recipes/` added as
the consequence of §2.1/§2.4, and `atomic/` promoted to its own package because every writer needs
it.

**Key interface**

```go
type Provider interface {
    // Resolve turns a version constraint into an immutable, reproducible pin.
    Resolve(ctx context.Context, dep manifest.Dependency) (lockfile.Package, error)
    // Materialize ensures the source is present locally, using the cache.
    Materialize(ctx context.Context, pkg lockfile.Package) (path string, err error)
}
```

Two implementations in the MVP: `git` and `path`. New sources become new implementations with no
change elsewhere.

---

## 7. On-disk layout

```
~/.cache/cppm/
├── git/github.com/fmtlib/fmt.git        bare mirror, shared across all projects
└── recipes/                              downloaded recipe updates

MyProject/
├── cppm.json          committed
├── cppm.lock          committed
├── CMakeLists.txt     committed — user-owned, never rewritten after init
├── CMakePresets.json  committed — generated, but stable
├── .cppm/             gitignored
│   └── generated/
│       ├── cppm_dependencies.cmake
│       └── cppm_provider.cmake
└── build/             gitignored
```

Global cache rather than the proposal's per-project `.cppm/dependencies/` — §3.7. `cppm init` writes
the `.gitignore` entries.

---

## 8. Command behaviour

| Command | Network | Writes | Notes |
|---|---|---|---|
| `init` | no | manifest, lock, CMakeLists, presets, src, .gitignore | `init .` supported |
| `add` | yes | manifest, lock, generated | Resolves ref→commit, clones to cache, infers targets. **May prompt** (Tier 5); `--target` / `--non-interactive` suppress it |
| `remove` | no | manifest, lock, generated | |
| `sync` | yes if lock incomplete | lock, generated | **Honours existing lock.** Idempotent |
| `update` | yes | lock, generated | The only command that moves pins |
| `build` | via sync | build/ | `cmake --preset`; `--release` `--debug` `--clean` |
| `list` | no | — | |
| `info <dep>` | no | — | Shows `target_origin`, so guesses are visible |
| `doctor` | no | — | Toolchain, manifest/lock drift, inferred-target warnings |
| `clean` | no | build/ | `--all` also clears the global cache |

The critical distinction, understated in the proposal: **`sync` must never change a pin.** If the
lock is valid it is obeyed exactly; it only fills gaps. Only `update` re-resolves. Getting this wrong
destroys reproducibility, so it should be an explicit test.

---

## 9. Revised roadmap

**Phase 0 — Discovery measurement. ✅ DONE.** [tools/phase0](../tools/phase0/main.go) clones ten real
libraries and reports inference accuracy with no recipes: **8 HIT, 2 ASK, 0 WRONG**. Exit criterion
(>80% ⇒ inference-first) met, so the architecture is inference-first with a small recipe DB. The
scan/veto/rank/gate logic is promoted to `internal/discover` in Phase 3, and the harness becomes its
regression test — its corpus is the natural fixture set.

**Phase 1 — Skeleton that builds something.** `go.mod`, cobra root, manifest + lock read/write with
atomic writes and deterministic ordering, `init`, and a generated `CMakeLists.txt` that configures
and builds a hello-world with zero dependencies. End-to-end from day one.

**Phase 2 — Git dependencies.** `vcs` git wrapper with argv safety and shallow fallback, global
mirror cache, tag→commit resolution, `add` / `remove`, lockfile pinning. Acceptance: `cppm add
github:fmtlib/fmt@11.1.4 && cppm build` works.

**Phase 3 — Discovery and CMake integration.** The three tiers, the recipe DB, the verification
loop, `sync`, local `path` dependencies, and the dependency provider. The heart of the project.
Acceptance: the proposal's §13 scenario — fmt + nlohmann/json + a local library — works verbatim.

**Phase 4 — Build and presets.** Presets generation, `build` as a preset wrapper, debug/release,
`doctor`, `list`, `info`.

**Phase 5 — Hardening.** Conflict detection and reporting, circular local-dependency detection,
cross-platform CI (Linux/macOS/Windows × GCC/Clang/MSVC), testscript coverage, idempotence tests,
docs and release binaries.

Phases 1–2 are mostly mechanical. Phase 3 carries nearly all the risk, which is why Phase 0 exists.

---

## 10. Known limitations to state publicly

Being explicit in the README about these is better than implying capability:

0. **cppm does not replace CMake and does not generate complete CMake.** It gets a project started
   and building without CMake expertise. Non-trivial projects will eventually edit
   `CMakeLists.txt` directly, and cppm is designed to stay out of the way when they do — it never
   rewrites that file after `init`.
1. **No version solving.** cppm pins; it does not solve. Conflicts are reported, not resolved.
2. **Transitive deduplication is best-effort** and depends on recipe knowledge —
   [evaluation.md](evaluation.md) §2.4.
3. **Non-CMake libraries need a recipe or a manual wrapper.** Autotools and Makefile projects are
   out of scope for the MVP.
4. **Inferred target names are ~80% accurate.** When inference is not confident it declines and
   tells you the one line to add, rather than guessing — [evaluation.md](evaluation.md) §2.5.
5. **Shallow fetch depends on server support,** with automatic fallback — §2.3.
