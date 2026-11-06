# cppm — Technical Evaluation of the Proposal

Status: pre-implementation review
Date: 2026-11-06
Reviewed document: [proposal.md](../proposal.md)
Environment verified: CMake 3.28.3 · Ninja 1.13.2 · Git 2.43.0 · GCC 13.3 · Clang 22.1 · Go 1.27.1

---

## 0. Scope boundary

This governs everything below, so it is stated first.

**cppm is a project scaffolder, a dependency pinner, and a wrapper over CMake presets and the
build. It is not a CMake replacement and does not claim to generate complete or final CMake.**

The goal is to let a developer *start* and run a C++/CMake project without knowing CMake syntax.
In some scenarios the user will open `CMakeLists.txt` and edit it by hand, and that is an expected,
supported outcome — not a failure of the tool.

One consequence for the proposal text: §1's "Developers should manage dependencies, not write
dependency-related CMake code" is stronger than what cppm should promise. The accurate version is
*"developers should not need to write dependency CMake to get started."* Worth softening in
[proposal.md](../proposal.md) so the README does not over-claim.

---

## 1. Verdict

The proposal is **sound and worth building.** Under the scope in §0 it is also materially less risky
than the original framing implied.

The feature the proposal treats as a solved subroutine — *automatic CMake target detection* — is the
hardest part of the project and is **not statically solvable in the general case** (§2.1). Under a
"never write CMake" promise that would be an existential problem. Under §0 it is not: a missed
target becomes a clear error message telling the user which line to edit, which is an acceptable and
documentable outcome. Everything else (manifest, lockfile, FetchContent generation, preset wrapping)
is straightforward engineering by comparison.

Recommendation: **proceed.** Adopt the findings in §3, keep the user/generated file boundary
absolute, and treat target inference as a *convenience with a good failure path* rather than a
correctness guarantee.

I ran four empirical spikes against real repositories to test the riskiest assumptions. Results are
in §2 and they change the architecture materially.

---

## 2. Spike results

### 2.1 CMake cannot enumerate the target names users are supposed to link

This is the central finding.

A project that `FetchContent`s fmt and then inspects the directory it created:

```
BUILDSYSTEM_TARGETS = fmt;fmt-header-only
IMPORTED_TARGETS    = (empty)
```

The canonical, documented link name is `fmt::fmt`. It does **not appear**. `fmt::fmt` is an `ALIAS`
target, and CMake provides no property or command that enumerates alias targets. The same holds for
spdlog (`BUILDSYSTEM_TARGETS = spdlog;spdlog_header_only`, canonical name `spdlog::spdlog`).

So "inspect the dependency and read its targets" does not work. Two further problems compound it:

- **Ambiguity.** Even with a full list, nothing marks which target is the intended public one. For
  fmt the choice is `fmt` vs `fmt-header-only`; for spdlog, `spdlog` vs `spdlog_header_only`. There
  is no machine-readable signal.
- **Probing works, enumeration does not.** `if(TARGET fmt::fmt)` correctly returns true, and
  `ALIASED_TARGET` resolves to `fmt`. Confirmed against seven candidate names, including two
  deliberate non-existent ones, with no false positives.

### 2.2 Candidate names *can* be recovered statically from source

Grepping the cloned source for alias declarations recovers exactly what enumeration missed:

```
fmt-src     → add_library(fmt::fmt ALIAS fmt)
               add_library(fmt::fmt-header-only ALIAS fmt-header-only)
spdlog-src  → add_library(spdlog::spdlog ALIAS spdlog)
               add_library(spdlog::spdlog_header_only ALIAS spdlog_header_only)
```

This is the unlock. It means target discovery does **not** require a throwaway CMake configure:
clone, grep for `add_library(X ALIAS Y)` and `add_library(X INTERFACE|STATIC|SHARED)`, rank the
candidates, then verify the winner with a cheap `if(TARGET ...)` probe at real configure time. Fast
enough to run inside `cppm add`.

Residual problem: ranking. `fmt::fmt` vs `fmt::fmt-header-only` still needs a heuristic plus an
override path. See [architecture.md](architecture.md) §4.

### 2.3 Shallow clone with a pinned commit works on GitHub — but is not portable

`GIT_SHALLOW ON` combined with a full commit hash succeeded: depth 1, 6.8 MB for fmt, HEAD exactly
at the requested commit. Verified the pin is the genuine tag:

```
123913715afeb8a437e6388b4473fcc4753e1c9a  refs/tags/11.1.4        (fmt)
6fa36017cfd5731d617e1a934f0e5ea9c4445b13  refs/tags/v1.15.3       (spdlog)
```

However this depends on the server enabling `uploadpack.allowReachableSHA1InWant`. GitHub does;
many self-hosted and corporate Git servers do not, and CMake's own documentation warns against
combining shallow with a hash. **Do not hardcode shallow.** Try shallow, detect failure, retry
unshallowed.

### 2.4 Transitive unification is per-package knowledge, not an algorithm

A project declaring fmt first and spdlog second configures and builds cleanly (13 build steps, no
test or example pollution once options are injected). But spdlog **used its own bundled copy of
fmt**, not the fmt that was already declared. Unifying them requires knowing one package-specific
cache variable, `SPDLOG_FMT_EXTERNAL=ON`. Nothing in the dependency's metadata advertises this.

The consequence is important for scoping: generic, automatic transitive deduplication is not
achievable. It is a curated-knowledge problem. This makes the recipe database (§3.1) load-bearing
rather than optional polish, and it means the proposal's "support transitive dependencies where
possible" should be stated honestly as a limitation rather than an objective.

---

### 2.5 Phase 0 measurement: inference resolves 8 of 10 real libraries

Built a harness ([tools/phase0](../tools/phase0/main.go)) that clones ten real libraries and runs
the scan-and-rank pipeline with **no curated data at all**. Final result:

```
LIBRARY        RESULT TOP PICK                           EXPECTED
fmt            HIT    fmt::fmt                           fmt::fmt
spdlog         HIT    spdlog::spdlog                     spdlog::spdlog
json           HIT    nlohmann_json::nlohmann_json       nlohmann_json::nlohmann_json
Catch2         HIT    Catch2::Catch2                     Catch2::Catch2WithMain | Catch2::Catch2
googletest     ASK    (no confident answer)              GTest::gtest_main | GTest::gtest
eigen          HIT    Eigen3::Eigen                      Eigen3::Eigen
abseil         ASK    (no confident answer)              absl::base | absl::strings
SDL2           HIT    SDL2::SDL2                         SDL2::SDL2
cxxopts        HIT    cxxopts::cxxopts                   cxxopts::cxxopts
tomlplusplus   HIT    tomlplusplus::tomlplusplus         tomlplusplus::tomlplusplus

HIT   (top-1 correct)      : 8/10   80%
ASK   (declined to guess)  : 2
WRONG (confident + wrong)  : 0      <-- the number that must stay 0
```

**80% automatic, 20% honest refusal, zero confident errors.** Under the §0 scope that is a good
result: the two refusals cost the user one line of JSON, which is exactly the hand-off §0 permits.

Two iterations produced that number, and both lessons are worth keeping:

**Iteration 1 — 60%, one confidently wrong answer.** Three libraries yielded *zero* candidates. The
cause was not the ranking heuristic (which was correct in 6 of 6 cases where candidates existed) but
candidate *extraction*: target names assembled from CMake variables.

```cmake
# nlohmann/json — both names are variables
add_library(${NLOHMANN_JSON_TARGET_NAME} INTERFACE)
add_library(${PROJECT_NAME}::${NLOHMANN_JSON_TARGET_NAME} ALIAS ${NLOHMANN_JSON_TARGET_NAME})
```

Resolving `project()` and single-valued `set()` definitions fixes this class completely:
`project(nlohmann_json)` plus `set(NLOHMANN_JSON_TARGET_NAME ${PROJECT_NAME})` yields exactly
`nlohmann_json::nlohmann_json`. That change alone took the result from 60% to 80%.

**Iteration 2 — eliminating the confident error.** abseil ranked `absl::abseil_test_dll` first and
reported it with confidence. A soft scoring penalty was not enough, so markers like `test`, `bench`,
`mock`, `example` and `dll` became a **hard veto**: a candidate carrying one is disqualified, not
merely demoted, and if nothing survives the tool answers ASK. The marker is ignored when it also
appears in the package's own name, so a library legitimately called *googletest* is not vetoed by
its own name.

**The residual 20% is a limit of *static scanning*, not of the tool.** Both refusals have the same
cause — targets created by helper macros rather than literal `add_library` calls:

```cmake
# googletest/cmake/internal_utils.cmake — bare name is a function parameter
add_library(${cmake_package_name}::${name} ALIAS ${name})
cxx_library(gtest "${cxx_strict}" src/gtest-all.cc)   # custom macro, not add_library
```

### 2.6 A configure-time pass recovers the cases static scanning cannot

An earlier draft of this document called the 20% a hard limit. That was wrong, and worth recording
as a correction.

The key observation is that these libraries hide only the **bare** name behind macros. The
**namespace** is a literal in the source:

```cmake
set(cmake_package_name GTest CACHE INTERNAL "")          # googletest
add_library(absl::${ABSL_CC_LIB_NAME} ALIAS ${_NAME})    # abseil — "absl" is literal
```

And once CMake has actually run, `BUILDSYSTEM_TARGETS` yields the raw names that macros generated.
Combining the two — statically harvested namespaces × configure-time raw names, plus a
`<ns>_` prefix-strip rule — produces candidates that `if(TARGET)` can verify. Measured on
googletest, which static scanning scored at **zero candidates**:

```
SPIKE_RAW               = gtest;gtest_main;gmock;gmock_main
SPIKE_VERIFIED_ALIASES  = GTest::gtest;GTest::gtest_main;GTest::gmock;GTest::gmock_main
```

`GTest::gtest_main` — the expected answer — recovered and verified. The prefix-strip rule is
precisely what abseil needs (`absl_base` → `base` → `absl::base`), so the same mechanism should
cover it.

The cost is one CMake configure, which cppm runs anyway at first build. So discovery becomes
two-stage: static scan for an instant answer at `add` time, configure-time probing as the fallback.

**This demotes the recipe DB from required-for-correctness to a performance and knowledge
optimisation.** It still earns its place — it makes `add` instant with no configure, and it carries
option knowledge no probe can ever discover, such as `SPDLOG_FMT_EXTERNAL=ON` (§2.4). But it is no
longer the only route to a working answer for macro-based projects.

### 2.7 Asking the user is a first-class resolution path

Worth stating explicitly, because the earlier draft treated inference failure as something to be
*reported* rather than *resolved*.

`cppm add` is an interactive command typed at a terminal, so when inference is not confident it can
simply present the ranked candidates and ask. This is strictly better than failing, and for genuinely
ambiguous cases it is better than inferring at all: `Catch2::Catch2` vs `Catch2::Catch2WithMain` is a
**user choice**, not an unknown, and the measurement scoring it as a "HIT" slightly flatters the tool.

Constraints on where this is allowed:

| Context | Prompt? | Why |
|---|---|---|
| `cppm add` | Yes | User-initiated, TTY present, candidates in hand |
| `cppm sync` / `build` | Only if TTY | Runs in CI, scripts and IDE integrations, where a prompt hangs the build |
| Inside CMake configure | Never | CMake cannot prompt; hence the `FATAL_ERROR` hand-off |

So: TTY detection, a `--non-interactive` flag, and a `--target` flag so the answer can be supplied
up front in scripted use.

**Design rule extracted:** *prefer ASK over a confident guess.* An inference tool that declines
gracefully and names the one line to edit fits §0; one that silently links the wrong target does
not. `WRONG` is the metric to protect in CI, not `HIT`.

**Incidental infrastructure note.** GitLab returned HTTP 403 on the first run and cloned normally on
the second — transient rate-limiting, not host incompatibility. Non-GitHub hosts work; network
failures need retry with backoff, as the proposal's §10 already requires.

---

## 3. Findings that change the design

### 3.1 Target discovery needs three tiers, not one

The proposal offers "automatic detection" with "explicit declarations when impossible". In practice
three tiers are required, in priority order:

1. **Explicit** — `targets` field in the manifest. Always wins. Must exist from day one.
2. **Recipe** — an embedded curated database for the long tail of popular libraries, carrying
   target names, required cache options, and known transitive quirks (`SPDLOG_FMT_EXTERNAL`,
   `ABSL_PROPAGATE_CXX_STD`, Catch2's extras path, etc.).
3. **Inferred** — static alias grep (§2.2), ranked, then probe-verified and written into the lock.

Tier 3 alone will not be sufficient, and the recipe DB is the difference between a tool that works
on fmt and a tool that works on real projects.

### 3.2 Per-dependency cache options are a required manifest field

Entirely absent from the proposal's schema. Without `BUILD_TESTING=OFF`,
`SPDLOG_BUILD_EXAMPLE=OFF`, `FMT_TEST=OFF` and friends, every dependency builds its own test
suites, examples and benchmarks, and contributes install rules to your project. This is not a
refinement — a tool that omits it produces visibly broken builds on the second dependency.

### 3.3 Adopt CMake 3.24 dependency providers

CMake 3.24+ supports `cmake_language(SET_DEPENDENCY_PROVIDER ...)` installed via
`CMAKE_PROJECT_TOP_LEVEL_INCLUDES`. It lets cppm **intercept `find_package()` and
`FetchContent_MakeAvailable()` calls made by dependencies themselves** and redirect them to
cppm-managed copies.

This is the single biggest technical lever available and the proposal does not mention it. It is the
only clean mechanism for handling a dependency that internally calls `find_package(fmt)`. It
requires raising the CMake floor from the proposed 3.21 to **3.24 minimum** (3.25 preferred). The
machine here has 3.28, and 3.24 shipped in 2022 — the cost is low and the capability gain is large.

### 3.4 `cppm build` should wrap CMake presets, not reimplement CMake invocation

The proposal already generates `CMakePresets.json` but then describes `build` as separately running
configure, generate and compile. Make `build` a thin wrapper over `cmake --preset` / `cmake --build
--preset`. Lower risk, less code, and it means VS Code CMake Tools, CLion and Visual Studio read
the same configuration the CLI uses — a real interoperability win for free.

### 3.5 `cppm_link_dependencies(MyProject)` is too blunt

Linking every dependency to one target ignores multiple targets, `PRIVATE`/`PUBLIC`/`INTERFACE`
distinctions, and test-only dependencies. Keep the convenience macro, but make the generated file
also expose per-dependency variables so users can wire things manually, and let the manifest declare
targets and their dependency sets.

### 3.6 Be explicit that cppm is a pinner, not a version solver

"Detect dependency conflicts" is listed as a requirement but left undefined. FetchContent has no
solver: the first declaration silently wins. If A wants fmt 10 and B wants fmt 11, you get one and
possibly a broken build.

For the MVP, **detect and report** the conflict and require explicit resolution via an `overrides`
block. Do not attempt constraint solving. Stating this limitation plainly in the README is better
than implying a capability that is not there.

### 3.7 Cache should be global, not per-project

The proposal places downloads in `.cppm/dependencies/` per project. Ten projects using fmt means ten
clones. Use a global mirror cache (`~/.cache/cppm/git/<host>/<owner>/<repo>.git`) and point
FetchContent at it. This affects on-disk layout and the lockfile, so it should be decided now rather
than retrofitted.

### 3.8 Version tags in C++ are not semver

Real tags observed: `11.1.4`, `v1.15.3`, `8.1.1`, `9.0.0`, plus `release-*` and `boost-*` forms
elsewhere. Any constraint handling needs a tolerant tag normalizer before it can compare versions.
Small, but real work that is easy to underestimate.

---

## 4. Assessment of the Go decision

Switching the implementation language from C++ to Go is the right call, for a reason beyond
convenience.

**It removes a bootstrap problem.** A C++ tool built with CMake, CLI11 and nlohmann-json needs
CMake, a toolchain and two dependencies to compile — meaning the tool that exists to cure C++
dependency pain would itself be painful to install. Go produces a single static binary, installs via
`go install`, and cross-compiles to linux/windows/darwin × amd64/arm64 from one machine. For a
developer tool, distribution is a feature.

**The work is in Go's sweet spot.** Everything cppm does is subprocess orchestration, JSON, file
I/O and Git. C++ offers no advantage in any of those, and the original stack's "modern C++20" was
incidental to the problem rather than useful for it.

**Recommended libraries**

| Need | Choice | Note |
|---|---|---|
| CLI | `spf13/cobra` | Also yields shell completions and man pages |
| JSON | `encoding/json` | Stdlib is sufficient; keep key order stable for clean diffs |
| Versions | `golang.org/x/mod/semver` | Wrap it — see §3.8, tags are not strict semver |
| Git | **git CLI via `os/exec`** | Not `go-git`. Git is already a hard requirement because FetchContent shells out to it, so a pure-Go implementation adds a second code path and lags on shallow-fetch and protocol-v2 behaviour |
| CLI tests | `rogpeppe/go-internal/testscript` | Script-driven golden tests; an excellent fit for this tool |

**Security note, inherited from the proposal.** The requirement "never construct shell commands by
blindly concatenating user input" is satisfied by construction in Go: `exec.Command` takes an argv
slice and does not invoke a shell. The remaining risks are argument injection (a ref or URL
beginning with `-` being read as a flag — terminate with `--` and validate) and path traversal from
`local:` dependencies. Both should be explicit validation rules.

**The one cost** is losing the "written in C++" dogfooding story. Functionally irrelevant; worth
noting only as a minor community-perception point. CMake itself being C++ has never been why people
use it.

---

## 5. Competitive position

Worth knowing precisely, because it determines where effort should go.

- **vcpkg** (already installed on this machine) and **Conan** — full package managers with curated
  registries. cppm should not try to compete here; the proposal is right to avoid a registry.
- **CPM.cmake** — the closest prior art. A FetchContent wrapper with source caching. It already
  solves caching ergonomics, but you still write CMake by hand.
- **cmkr** — generates `CMakeLists.txt` from TOML. Overlaps on the "don't write CMake" axis but has
  no lockfile and no dependency CLI.
- **Hunter**, **build2**, **xmake** — different models, smaller reach.

cppm's defensible gap is the **combination** of project scaffolding, a CLI-driven manifest, a true
commit-pinned lockfile, and a preset/build wrapper. No existing tool does all four.

Given §0, the claim to compete on is **time-to-first-successful-build for someone who does not know
CMake** — `cppm init` → `cppm add` → `cppm build` with no CMake authoring required to get going.
That is a different axis from CPM.cmake, which assumes you are already writing CMake and makes
dependencies less painful within it. It is adjacent to cmkr, which generates CMakeLists from TOML
but has no lockfile, no dependency CLI and no build wrapper.

Caching is table stakes; CPM.cmake already has it. The engineering effort should go to scaffolding
quality, reproducibility, and the quality of the failure path when inference declines (§2.5).

---

## 6. Recommended changes to the phase plan

The proposal's Phase 1→5 ordering builds the easy, low-risk parts first and defers the single
feature that determines whether the product works at all. Invert that.

**Phase 0 — Discovery measurement. DONE.** Result in §2.5: 80% automatic, 0 confident errors. The
exit criterion was "above ~80% means inference-first with recipes as fallback is viable" — met. The
architecture is therefore **inference-first with a small curated recipe DB**, as built in
[architecture.md](architecture.md) §4, and not recipe-first.

Phases 1–5 then follow roughly as proposed, with `init` and the manifest coming along nearly free,
and CMake generation pulled forward to sit beside `add`.

See [architecture.md](architecture.md) for the revised schemas, package layout and roadmap.

---

## 6b. Remaining risk register

> **Scope note.** v1 is deliberately three things — start a project, download a dependency, wire it
> into CMake ([architecture.md](architecture.md) §0, "v1 scope"). Under that scope **A1, A2, A3, B1
> and B2 below are deferred or accepted as documented limitations rather than solved.** They are
> kept here because they are real and will matter at v2, and because knowing *why* they are
> acceptable is what makes deferring them a decision rather than an oversight.
>
> What still needs care in v1: **B3** (how much to model, since `init`'s template sets the
> expectation) and the generated CMake interface, which is a compatibility surface cppm cannot
> migrate later because it does not own the user's `CMakeLists.txt`
> ([architecture.md](architecture.md) §5).

What Phase 0 and the spikes have **retired**: target discovery (§2.1–2.7), per-dependency option
injection (verified — suppressing `BUILD_TESTING` and `SPDLOG_BUILD_EXAMPLE` produced a clean
13-step build with no test or example targets), commit pinning with shallow fallback (§2.3), and
alias probing (§2.1).

What remains, ranked by risk rather than by order of work.

### A1. The lockfile does not cover transitive dependencies — *highest remaining risk*

cppm pins what **cppm** fetches. When spdlog internally resolves its own fmt — via its bundled copy,
its own `FetchContent`, or `find_package` — cppm neither pins it nor records it. So `cppm.lock` is an
*incomplete* record of the build, which partly undermines the reproducibility claim that is one of
the project's two differentiators (§5).

The fix is the CMake 3.24 dependency provider (§3.3): the provider intercepts every nested
`find_package()` and `FetchContent_MakeAvailable()`, lets cppm resolve and pin it, and records it in
the lock. That turns the lock into a complete record *and* delivers conflict detection as a
by-product, since the provider sees every version request.

**Unspiked.** This is the highest-value remaining piece of engineering and the one most likely to
hold surprises. It should be proven before Phase 3 is scoped.

### A2. `add_subdirectory` makes dependency code part of your build

FetchContent's model is not isolation — it is inclusion. Dependencies can therefore:

- set cache variables that affect you (`BUILD_SHARED_LIBS`, `CMAKE_CXX_STANDARD`,
  `CMAKE_POSITION_INDEPENDENT_CODE`);
- mutate `CMAKE_CXX_FLAGS` or call `add_compile_options()` at directory scope;
- **collide on target names** — two dependencies each defining `uninstall`, `docs` or `benchmark`
  is a hard configure error with no clean workaround;
- register `install()` rules that pollute your install tree.

`SYSTEM` and `EXCLUDE_FROM_ALL` mitigate some of this but need CMake 3.25/3.28 (§C1), and neither
prevents name collisions. Partially unavoidable; needs a spike to find where the real walls are and
what `cppm doctor` should warn about.

### A3. MSVC runtime library mismatch

The classic Windows failure: dependencies built `/MD` linked against a `/MT` consumer, producing
link errors that look unrelated to their cause. Needs `CMAKE_MSVC_RUNTIME_LIBRARY` propagated
consistently to every dependency. **Entirely untested** — every measurement in this document is
Linux/GCC/Clang.

### B1. Version constraints without a registry

There is no index to query, so "what versions exist" means `git ls-remote --tags` plus
normalisation — and C++ tags are not semver:

```
fmt         11.1.4          spdlog   v1.15.3
SDL         release-2.30.9  abseil   20240722.0     (a date)
```

Strict semver comparison breaks on at least two of those.

**Resolved by declining the responsibility.** cppm treats a version as an **opaque git ref** and
never parses, orders or compares it — see [architecture.md](architecture.md) §3b. A library that does
not follow semver is therefore not cppm's problem. This removes the normaliser, ranges, and version
ordering entirely; `update` re-resolves refs rather than seeking newer releases, and a moved tag is
reported by hash comparison, which needs no version semantics at all.

The only residual choice is what bare `cppm add fmt` means: newest tag by git creation date, with
the chosen ref echoed to the user. Its known failure mode (a back-tagged old series) is documented
rather than fixed.

### B2. Conflict resolution UX

Detection becomes feasible via A1's provider, but it is configure-time and after the fact. The open
question is what happens *next*: how an `overrides` entry is surfaced, suggested and applied without
requiring the user to understand FetchContent's first-declaration-wins rule.

### B3. How much build-system modelling to absorb — *main scope risk*

The manifest's `targets` section is the top of a slope: sources today, then globs, per-target
include directories, compile definitions, test registration — at which point cppm has become a
build-system DSL and competes with cmkr, contradicting §0.

A defensible line: cppm models **just enough to scaffold and link** — target kind, sources,
dependency sets — and anything beyond that is the user's `CMakeLists.txt`. Worth writing down
before `init` is implemented, because the template is what sets the expectation.

**v1 answer:** one target, an explicit source list, no globs. If a project outgrows that, the user
edits `CMakeLists.txt` — which §0 already establishes as a supported outcome rather than a failure.
The manifest keeps a `targets` section so the schema can grow, but v1 neither generates nor reads
more than the single-target case.

### C1. The generator must target a range of CMake versions

Found while reviewing this document: `EXCLUDE_FROM_ALL` on `FetchContent_Declare` requires **3.28**
and `SYSTEM` requires **3.25**, while §3.3 recommends a **3.24** floor for dependency providers. The
generated file must therefore feature-detect rather than assume, and cppm needs a small compatibility
matrix of which keywords it may emit at which floor. Mechanical, but a real source of "works on my
machine" bugs.

### C2. Cross-platform specifics

Windows `MAX_PATH` limits against deep `_deps/<name>-subbuild` paths, case-insensitive filesystems,
generator differences, and shell quoting. Tedious rather than conceptually hard, but it is where the
bug reports will come from.

### C3. Local path dependencies

Circular-dependency detection, whether a local dependency that is *itself* a cppm project gets its
manifest read recursively, and what locking means for something editable by design.

---

## 7. Decisions taken

1. **Implementation language: Go** (§4). Replaces the proposal's C++20.
2. **Scope: scaffolder + pinner + preset wrapper**, not a CMake replacement (§0).
3. **Architecture: inference-first, with a small embedded recipe DB** of ~20–30 libraries.
   Justified by the Phase 0 measurement (§2.5) rather than by assumption.
4. **CMake floor raised to 3.24** to permit dependency providers (§3.3).
5. **Module path `cppm`** as a placeholder; renaming later is a one-line change plus a find-replace.
6. **Repository initialised**, docs and the Phase 0 harness committed.

### Still open

1. **Recipe DB contents** — which ~20–30 libraries ship in the embedded set. googletest and abseil
   are required by §2.5; the rest should follow observed demand.
2. **`proposal.md` §1 wording** over-claims relative to §0 and should be softened.
3. **Windows/macOS verification** — all measurements here are Linux. MSVC in particular will differ.
