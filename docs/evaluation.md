# cppm — Technical Evaluation of the Proposal

Status: pre-implementation review
Date: 2026-11-06
Reviewed document: [proposal.md](../proposal.md)
Environment verified: CMake 3.28.3 · Ninja 1.13.2 · Git 2.43.0 · GCC 13.3 · Clang 22.1 · Go 1.27.1

---

## 1. Verdict

The proposal is **sound and worth building**, with one serious caveat: the feature it treats as a
solved subroutine — *automatic CMake target detection* — is the hardest part of the project and is
not statically solvable in the general case. Everything else (manifest, lockfile, FetchContent
generation, build wrapping) is straightforward engineering by comparison.

Recommendation: **proceed**, but reorder the plan to de-risk target discovery first, and adopt the
three findings in §3 before writing production code.

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

cppm's defensible gap is the **combination** of a CLI-driven manifest, a true commit-pinned
lockfile, and automatic target wiring. No existing tool does all three. But note the implication:
because CPM.cmake already covers caching plus FetchContent convenience, cppm must win specifically
on *never hand-writing CMake* and on *reproducibility*. Those two should receive the engineering
effort; caching is table stakes.

---

## 6. Recommended changes to the phase plan

The proposal's Phase 1→5 ordering builds the easy, low-risk parts first and defers the single
feature that determines whether the product works at all. Invert that.

**Phase 0 — Discovery measurement (new, do this first).**
Before committing to the architecture, measure what fraction of real libraries can be resolved
automatically by the §2.2 method. Suggested sample: fmt, spdlog, nlohmann/json, Catch2, googletest,
Eigen, abseil, SDL2, cxxopts, tomlplusplus.

This number decides the product. Above ~80%, inference-first with recipes as fallback is viable.
Below ~70%, the value proposition shifts to the curated recipe database and scope changes
significantly. It is cheap to measure and expensive to guess wrong about.

Phases 1–5 then follow roughly as proposed, with `init` and the manifest coming along nearly free,
and CMake generation pulled forward to sit beside `add`.

See [architecture.md](architecture.md) for the revised schemas, package layout and roadmap.

---

## 7. Open items requiring a decision

1. **Go module path** — needed for `go.mod`; blocks the first commit.
2. **Recipe database in the MVP, or inference only?** Materially changes scope. §2.4 argues it is
   not optional, but it is a real cost.
3. **CMake floor: 3.24+** to permit dependency providers (§3.3). Recommended.
4. **Repository is not yet a Git repo** — `git init` is pending.
