---
name: cppm
description: Create, configure and build C++ projects with CMake using the cppm CLI. Use when the user wants to start a new C++ project, add a C++ library or dependency (fmt, spdlog, nlohmann/json, Catch2, googletest, Eigen, SDL, Boost and similar), set the C++ standard or compiler, wire a dependency into CMake, or build a CMake project — and whenever a project directory contains cppm.json. Also use instead of hand-writing FetchContent, find_package or target_link_libraries calls for third-party libraries.
---

# Using cppm

cppm is a CLI that scaffolds C++ projects, pins git dependencies to commits, generates the CMake
wiring, and wraps `cmake --preset`. It is **not** a package manager with a registry, and it does not
replace CMake.

Check availability with `cppm --version`. If it is absent, install it:

```sh
curl -fsSL https://raw.githubusercontent.com/mehdi-shokohi/cppm/main/install.sh | sh   # Linux/macOS
go install github.com/mehdi-shokohi/cppm/cmd/cppm@latest                               # with Go
```

## Always pass --non-interactive

`cppm add` prompts when a dependency has no `@ref`, or when it cannot determine the link target.
In an automated context that prompt will **hang forever**. So:

```sh
cppm add github:fmtlib/fmt@11.1.4 --non-interactive
```

Better still, avoid the prompt entirely by always pinning an explicit `@tag`. If you do not know the
tag, look it up first rather than letting cppm ask:

```sh
git ls-remote --tags https://github.com/fmtlib/fmt.git | tail -20
```

## Core workflow

```sh
cppm init MyApp --std 20          # scaffold; builds immediately
cd MyApp
cppm add github:fmtlib/fmt@11.1.4 --non-interactive
cppm build                        # fetches, configures, compiles
./build/debug/MyApp
```

Then write code in `src/main.cpp` and `#include <fmt/core.h>` as normal. No CMake editing is needed
to use the dependency.

With tests:

```sh
cppm init MyApp --std 20 --with-tests
cd MyApp
cppm add github:catchorg/Catch2@v3.7.1 --dev --non-interactive
cppm test
```

The scaffolded test uses no framework, so it runs before any dependency exists. `--dev` is what
keeps Catch2 out of the release binary.

## Command reference

Every flag below accepts one or two leading dashes (`-std` or `--std`), and flags may appear before
or after positional arguments.

### cppm init [&lt;directory&gt;]

Scaffolds a project that configures and builds immediately. With no directory, or `.`, initialises
the current directory; otherwise the directory is created. Existing files are never clobbered.

| Flag | Meaning |
|---|---|
| `--std <n>` | C++ standard: 11, 14, 17, 20, 23 or 26 (default 20) |
| `--name <s>` | project name (default: the directory name) |
| `--compiler <s>` | `clang++`, `g++`, or a path (default: CMake's choice) |
| `--generator <s>` | CMake generator (default: Ninja when installed) |
| `--lib` | scaffold a library instead of an executable |
| `--with-tests` | also scaffold `tests/`, a first test and the `add_test()` wiring |
| `--force` | overwrite an existing `cppm.json` |

### cppm add &lt;spec&gt;

Resolves a dependency, pins it to a commit, and wires it into CMake.

| Flag | Meaning |
|---|---|
| `--non-interactive` | never prompt; record unresolved targets instead |
| `--target <s>` | CMake target(s) to link, comma-separated; skips discovery |
| `--dev` | test-only; never linked into a shipping target |
| `--name <s>` | dependency name (default: derived from the repository) |
| `--no-discover` | do not download the source to look for targets |
| `--force` | replace an existing dependency of the same name |

Specs: `github:owner/repo@tag` · `gitlab:owner/repo@tag` · `codeberg:owner/repo@tag` ·
`https://host/path.git@ref` · `local:../SiblingLib`

### cppm remove &lt;dep&gt;... *(alias: `rm`)*

Removes dependencies from `cppm.json` and `cppm.lock` and regenerates the CMake. No flags. It does
not edit `CMakeLists.txt`, so remove any `target_link_libraries` lines you added by hand.

### cppm sync *(aliases: `apply`, `regen`)*

Applies `cppm.json` to the generated CMake and `CMakePresets.json`, then reports what changed.
Run it after editing the manifest. Never moves a pin.

| Flag | Meaning |
|---|---|
| `--quiet` | only report problems |

### cppm build

Syncs, configures and builds, by wrapping `cmake --preset` and `cmake --build --preset`.

| Flag | Meaning |
|---|---|
| `--debug` | build the debug preset (default) |
| `--release` | build the release preset |
| `--clean` | delete the build directory first |
| `--jobs <n>` | parallel jobs (default: let the backend decide) |
| `--target <s>` | build only this target |
| `--verbose` | show the compiler command lines |

Output lands in `build/debug/` or `build/release/`.

### cppm test

Builds, then runs the tests by wrapping `ctest --preset`. Builds first on purpose: a test run
against a stale build tree reports on code that is no longer there.

| Flag | Meaning |
|---|---|
| `--debug` | test the debug preset (default) |
| `--release` | test the release preset |
| `--filter <re>` | run only tests whose name matches this regular expression (`ctest -R`) |
| `--jobs <n>` | run this many tests in parallel |
| `--verbose` | show output from passing tests too |
| `--list` | list the tests without running them |
| `--no-build` | do not build first; test whatever is in the build tree |
| `--fail-fast` | stop at the first failing test |

Failing tests always print their own output; the generated test presets set `outputOnFailure`.

### cppm update [&lt;dep&gt;...]

Re-resolves refs to their current commits. With no arguments, all dependencies.

| Flag | Meaning |
|---|---|
| `--dry-run` | report what would change without writing |

### cppm list

| Flag | Meaning |
|---|---|
| `--long` | also show resolved commits and target origin |

The origin column matters: `explicit` and `recipe` are reliable, `inferred` was a heuristic guess.

### cppm info &lt;dep&gt;

Shows the resolved ref, commit, targets and how the target was determined. No flags.

### cppm doctor

Diagnoses the toolchain and the project. Exits non-zero on failure, so it is usable in CI.
**Run this first whenever something does not work.** No flags.

### cppm clean

| Flag | Meaning |
|---|---|
| `--all` | also delete `.cppm/` generated files |

### Global

`cppm --version` · `cppm help` · `cppm help <command>`

Commands other than `init` work from any subdirectory: cppm walks up to find `cppm.json`, like git.

### Environment

| Variable | Effect |
|---|---|
| `CPPM_CACHE_DIR` | relocate the global source cache (default `~/.cache/cppm`) |
| `CPPM_RECIPES_DIR` | extra recipe directory; overrides the built-in database |
| `CXX` | consulted when detecting a compiler |

## Test frameworks always get --dev

A test framework added without `--dev` is linked into every target, including the one that ships:

```sh
cppm add github:catchorg/Catch2@v3.7.1 --dev --non-interactive   # right
cppm add github:catchorg/Catch2@v3.7.1 --non-interactive         # wrong
```

This applies to Catch2, googletest, doctest and benchmark.

A `--dev` dependency is linked into the targets marked `"test": true` in `cppm.json` — nothing else.
If the project has no such target, `cppm add --dev` says so; add one with
`cppm init --with-tests`, or mark an existing target by hand in `cppm.json` and run `cppm sync`:

```json
"targets": {
  "MyApp_tests": { "kind": "executable", "sources": ["tests/MyApp_test.cpp"], "test": true }
}
```

Marking it is not cosmetic: that declaration is the only thing that routes dev dependencies to the
target. cppm never infers it from a name like `foo_tests`.

Writing a test target into `CMakeLists.txt` by hand is fine — anything registered with `add_test()`
is run — but that target will not receive dev dependencies unless `cppm.json` also declares it.

## Two rules that decide whether you use cppm correctly

**1. To add a third-party library, run `cppm add` — never hand-write CMake for it.**
Do not add `FetchContent_Declare`, `find_package` or `target_link_libraries(... fmt::fmt)` yourself.
cppm generates all of that into `.cppm/generated/cppm.cmake`, and anything you write by hand will
duplicate it or conflict with it.

**2. To change a project setting, edit `cppm.json` and run `cppm sync` — not `CMakeLists.txt`.**
The C++ standard, compiler, generator and build types all live in `cppm.json`:

```json
{
  "cxx":       { "standard": 20, "required": true, "extensions": false },
  "cmake":     { "minimum": "3.24", "exportCompileCommands": true,
                 "buildTypes": { "debug": "Debug", "release": "RelWithDebInfo" } },
  "toolchain": { "cxx": "g++", "c": "gcc", "generator": "Ninja" }
}
```

Setting `CMAKE_CXX_STANDARD` in `CMakeLists.txt` after the cppm include **silently overrides**
`cppm.json` and makes `cppm sync` appear to do nothing. `cppm doctor` warns about this.

### What you *should* edit in CMakeLists.txt

Your own targets, sources, include directories and `project()` — cppm writes that file once at
`init` and never touches it again. Adding a second executable or your own test target belongs there.

## When a dependency's target cannot be resolved

cppm declines rather than guessing, because a wrong link target produces a build that configures and
links the wrong thing. You will see:

```
targets:  unresolved
```

Fix it by naming the target in `cppm.json`, then syncing:

```json
"mylib": { "source": "git", "url": "...", "version": "v1.0", "targets": ["mylib::mylib"] }
```

```sh
cppm sync && cppm build
```

To find the real name, grep the dependency's source for its alias:

```sh
grep -rn 'add_library([^)]*ALIAS' ~/.cache/cppm/src/<host>/<owner>/<repo>/*/
```

A target reported as `inferred` by `cppm list --long` was a heuristic guess — if linking fails, pin
it explicitly as above.

## Versions are opaque git refs

cppm never parses or compares version strings, so:

- **Ranges do not exist.** `^11.0` and `>=3` are not supported. Use an exact tag, branch or commit.
- **`cppm update` does not find newer releases.** It re-resolves the ref you gave: a branch advances,
  a tag normally does not. To move to a new release, `cppm add <spec>@<newtag> --force`.
- Tags like `20240722.0` or `release-2.30.9` work fine, because nothing is ever compared.

## Dependency options

A dependency that builds its own tests or examples wastes build time. Suppress it per dependency:

```json
"somelib": { "source": "git", "url": "...", "version": "v1.0",
             "options": { "SOMELIB_BUILD_TESTS": "OFF" } }
```

cppm already knows the right options for ~25 common libraries, so this is usually unnecessary.

## Files

| Path | Owner | Commit it |
|---|---|---|
| `cppm.json` | you | yes |
| `cppm.lock` | cppm | yes — it pins commits for reproducible builds |
| `CMakeLists.txt` | **you** | yes — cppm never rewrites it |
| `tests/` | you | yes — scaffolded by `--with-tests`, then yours |
| `CMakePresets.json` | cppm | yes |
| `.cppm/`, `build/` | cppm | no, git-ignored |

Never edit `.cppm/generated/cppm.cmake`; it is regenerated on every sync.

## Troubleshooting

| Symptom | Action |
|---|---|
| anything unexpected | `cppm doctor` |
| `does not provide target 'X'` at configure | pin the real target in `cppm.json`, then `cppm sync` |
| `cppm sync` seems to do nothing | `CMakeLists.txt` is probably overriding the setting |
| compiler change not taking effect | `cppm build` detects it and reconfigures; else `cppm build --clean` |
| standard rejected | `cppm doctor` reports whether your compiler supports it |
| hangs with no output | a prompt is waiting — rerun with `--non-interactive` |
| `cppm test` says the project has no tests | `cppm init --with-tests`, or add `add_test()` to `CMakeLists.txt` |
| undefined references to Catch2 in a test | the target is not marked `"test": true` in `cppm.json` |
| a dev dependency appears in a release binary | it was added without `--dev`; add `"dev": true` and `cppm sync` |
