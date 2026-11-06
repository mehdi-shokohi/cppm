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
| `--force` | overwrite an existing `cppm.json` |

### cppm add &lt;spec&gt;

Resolves a dependency, pins it to a commit, and wires it into CMake.

| Flag | Meaning |
|---|---|
| `--non-interactive` | never prompt; record unresolved targets instead |
| `--target <s>` | CMake target(s) to link, comma-separated; skips discovery |
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
