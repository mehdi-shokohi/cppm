# Contributing to cppm

Thanks for your interest. This covers how to build and test cppm, and the few conventions that
matter more here than in a typical Go project.

Start with [architecture.md](architecture.md) — in particular §1 (what cppm is), §2 (the design
rules) and §3 (the target-discovery problem). Most review comments on a first contribution trace
back to one of those.

---

## Getting set up

```bash
git clone https://github.com/mehdi-shokohi/cppm
cd cppm
go build -o cppm ./cmd/cppm
go test ./...
```

You need **Go 1.23+**. To run the full test suite you also need **CMake 3.24+**, **Git**, a **C++
compiler**, and ideally **Ninja**. `./cppm doctor` checks all of them.

### cppm has no third-party Go dependencies, and should not gain any

`go.sum` is empty, and CI fails if it is not. This is not minimalism for its own sake: cppm exists
because C++ dependency handling is painful, so a tool that was itself awkward to build would be
hard to take seriously. It also means `go build` works offline, on any machine, forever.

If you believe a dependency is genuinely necessary, open an issue first and make the case. The CLI
surface is small and regular enough that a framework has not been needed.

---

## Running tests

```bash
go test ./...                  # everything, including end-to-end (~80s)
go test -short ./...           # skips network and compiler-heavy tests (fast)
go test -race -short ./...     # what CI runs for data races
go test ./internal/discover/   # one package
go test -run TestVeto ./...    # one test
```

Two environment variables help:

| Variable | Effect |
|---|---|
| `CPPM_SKIP_NETWORK_TESTS=1` | skip tests that reach GitHub |
| `CPPM_CACHE_DIR` | relocate the source cache; tests set this to a temp dir |

Tests that need the network skip themselves cleanly when it is unavailable, so a plane or a
locked-down network should never produce a red suite. If you add such a test, call the `network(t)`
helper in [internal/e2e/add_test.go](../internal/e2e/add_test.go).

### The test layout

- **Unit tests** live beside the code and use synthetic fixtures. The discovery tests build small
  fake CMake trees in `t.TempDir()`, so they need no network and run in milliseconds.
- **End-to-end tests** in `internal/e2e` build the real binary and drive it against real CMake and a
  real compiler. These are the ones that catch what actually matters.

---

## What the tests are really protecting

A few tests look fussy but guard decisions that are expensive or impossible to reverse. Please do
not weaken them without reading the reasoning.

**`WRONG` must stay zero.** In target discovery, a confidently wrong answer is far worse than a
refusal: it produces a build that configures, links, and is silently wrong. During development
abseil ranked `absl::abseil_test_dll` first and recommended it. The hit rate is a nice-to-have; the
absence of confident errors is the actual requirement.

**The generated CMake interface cannot change.** `cppm_link_dependencies`, `CPPM_TARGETS_<dep>`,
`CPPM_DEPENDENCIES` and the include path live inside a user-owned `CMakeLists.txt` that cppm
promised never to rewrite — so cppm can never migrate them. `TestPublicInterfaceIsStable` exists to
make breaking them a deliberate act. Anything new must be prefixed `_cppm_`.

**Cache options must precede `FetchContent_Declare`.** They are silently inert afterwards, and the
result is every dependency building its own test suite. Tested by
`TestOptionsArePlacedBeforeDeclare`.

**`sync` must be idempotent and must never touch `CMakeLists.txt`.** Two tests cover this. If a
change makes `sync` rewrite a user-owned file, that is a design error, not a test to update.

**`sync` must never move a pin.** Only `update` re-resolves. Getting this wrong destroys
reproducibility silently.

**Serialisation must be deterministic.** Manifests and lockfiles are committed to git; unstable key
order produces noisy diffs and merge conflicts.

---

## Code conventions

**Comments explain *why*, never *what*.** `i++ // increment i` is noise. The useful comment is the
one recording a constraint that is not visible from the code — that `CMAKE_CXX_COMPILER` must be set
before `project()` runs, or that a soft scoring penalty was tried and was not enough. If a reader
would reasonably ask "why is this here?", answer it.

**Errors tell the user what to do.** Not `invalid target`, but which line of which file to change.
cppm is aimed at people who do not know CMake, so an error that only makes sense to a CMake expert
has failed.

**No shell.** Always `exec.Command` with an argv slice. Reject arguments beginning with `-` rather
than trying to escape them: git reads `--upload-pack=...` as remote code execution. See
architecture.md §7 before touching `internal/vcs`.

**Atomic writes.** Manifests, lockfiles and generated CMake go through `internal/atomicfile`. A
truncated `cppm.lock` is worse than a missing one.

**Prefer declining to guessing.** This applies beyond discovery. When cppm cannot be sure, it should
say so and name the fix.

---

## Adding a recipe

The most useful small contribution. [internal/recipes/recipes.json](../internal/recipes/recipes.json)
holds curated knowledge for common libraries. A recipe is worth adding when either:

1. The library's targets are built inside CMake helper macros, so no scanner can find them
   (googletest, abseil), or
2. It needs a cache option with **no discoverable signal** — spdlog quietly compiles its own bundled
   copy of fmt unless `SPDLOG_FMT_EXTERNAL=ON`, and nothing in its metadata hints at that.

```json
"https://github.com/owner/repo.git": {
  "targets": ["repo::repo"],
  "alternatives": ["repo::repo-header-only"],
  "options": { "REPO_BUILD_TESTS": "OFF" },
  "notes": "Anything surprising a user should know."
}
```

Please verify it actually works before submitting:

```bash
go build -o cppm ./cmd/cppm
./cppm init Test && cd Test
../cppm add github:owner/repo@<tag>
# add an #include to src/main.cpp
../cppm build
```

A recipe with an empty `targets` list is a legitimate answer — it records that the library exports
nothing usable — but it **must** carry a `notes` explaining why, and a test enforces that.

Do not add a recipe just because discovery already handles the library. The database is a shortcut
for the cases discovery cannot reach, not a registry.

---

## Submitting a change

1. Branch from `main`.
2. Make the change, with tests. A bug fix should come with a test that fails before it.
3. Run `gofmt -w .`, `go vet ./...`, `go test ./...`.
4. Write a commit message that explains **why**, not just what. If you discovered something
   non-obvious — a CMake constraint, a wrong assumption — put it in the message. That is where the
   next person will look.
5. Open a pull request describing the problem you hit and how you chose to solve it.

CI runs lint, `staticcheck`, tests on Linux, macOS and Windows, the race detector, coverage, a
cross-build of all six release targets, and both installer scripts.

### Things that need discussion first

Please open an issue before:

- **Adding a Go dependency** (see above).
- **Changing the generated CMake interface** — it cannot be migrated.
- **Adding version comparison of any kind.** cppm deliberately treats a version as an opaque git
  ref. Ranges and "find the newest release" are not missing features; they are declined scope, and
  that is what lets libraries which ignore semver work at all.
- **Making cppm write to `CMakeLists.txt`.** Design rule 2.
- **Broadening what cppm models** — sources, globs, per-target flags. The line is "just enough to
  scaffold and link"; past that, cppm becomes a build system and competes with CMake rather than
  wrapping it.

None of these are forbidden. They are decisions with consequences that outlive the pull request, so
they deserve a conversation rather than a surprise diff.

---

## Reporting bugs

Please include the output of `cppm doctor`, your `cppm.json`, what you expected, and what happened.
If it involves a dependency, the exact `cppm add` command matters — and if the problem is a wrong
CMake target, say which target turned out to be correct, since that may simply be a missing recipe.
