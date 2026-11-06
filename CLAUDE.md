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
   prefix.
4. **Never add version parsing, comparison or ranges.** A version is an opaque git ref. This is
   declined scope, not a missing feature: it is what lets libraries that ignore semver work.
5. **Never use a shell.** `exec.Command` with an argv slice. Reject arguments beginning with `-`
   rather than escaping them — git reads `--upload-pack=...` as remote code execution.
6. **Never let `sync` move a pin.** Only `update` re-resolves. Getting this wrong destroys
   reproducibility silently.
7. **Emit cache options before `FetchContent_Declare`.** They are inert afterwards, and the result
   is every dependency building its own test suite.
8. **Prefer declining to guessing.** A confidently wrong link target produces a build that
   configures, links, and is silently wrong. In discovery, `WRONG == 0` matters more than the hit
   rate.

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
| `internal/cmakegen` | the CMakeLists template and the managed include |
| `internal/toolchain` | locates and runs cmake/ninja/git/compiler |
| `internal/e2e` | drives the real binary against real CMake |

## Commit messages

Explain **why**, not what — the diff shows what. If you discovered something non-obvious, a CMake
constraint or a wrong assumption, put it in the message; that is where the next person will look.
Do not pad with bullet summaries of each changed file.
