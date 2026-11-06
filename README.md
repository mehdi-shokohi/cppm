# cppm

Start a C++ project, add a dependency, build it — without writing CMake.

```bash
cppm init MyApp
cd MyApp
cppm build
```

## What cppm is

A **project scaffolder, a dependency pinner, and a wrapper over CMake presets and the build.**

cppm generates CMake and shells out to `cmake`, `ninja` and `git`. It is not a build system and it
does not replace CMake.

## What cppm is not

**cppm does not claim to generate complete or final CMake.** The goal is to get you from nothing to
a building project without knowing CMake syntax. Non-trivial projects will eventually open
`CMakeLists.txt` and edit it, and that is expected and supported — cppm writes that file once, at
`init`, and never touches it again.

Also deliberately out of scope:

- **No version solving.** cppm pins; it does not solve. A version is an opaque git ref — a tag,
  branch or commit — and cppm never parses, orders or compares it. A library that does not follow
  semver is therefore not a problem.
- **No ranges.** `^11.0` and `>=3` are not supported, for the same reason.
- **Transitive dependencies are not pinned by cppm.** If a dependency fetches its own dependencies,
  cppm neither pins nor records them, so `cppm.lock` covers only what cppm fetches directly.
- **Non-CMake libraries** need a hand-written wrapper.

## Status

v1 is three things: start a project, download a dependency, wire it into CMake.

| Working | Not yet |
|---|---|
| `init` — scaffold a project that builds | `add` / `remove` |
| `sync` — regenerate managed CMake | dependency fetching |
| `build` — configure and build via presets | target discovery (designed and measured; not yet wired) |
| `list`, `info` | Windows / macOS verification |
| `doctor` — diagnose toolchain and project | |
| `clean` | |

## Commands

```
cppm init [<dir>] [--std 20] [--name N] [--lib] [--force]
cppm sync [--quiet]
cppm build [--debug|--release] [--clean] [--jobs N] [--target T] [--verbose]
cppm list [--long]
cppm info <dependency>
cppm doctor
cppm clean [--all]
```

Flags work before or after positional arguments, so `cppm init MyApp --std 17` does what it looks
like.

## Project layout

```
MyApp/
├── cppm.json          what the project wants          (commit this)
├── cppm.lock          what will be fetched             (commit this)
├── CMakeLists.txt     yours — cppm never rewrites it   (commit this)
├── CMakePresets.json  debug and release presets        (commit this)
├── src/main.cpp
├── include/
├── .cppm/             generated files                  (git-ignored)
└── build/             build output                     (git-ignored)
```

## The generated CMake interface

`init` writes an `include()` of cppm's generated file into your `CMakeLists.txt`. That file exposes
exactly four public names, which is a compatibility surface cppm will not change:

| Name | Purpose |
|---|---|
| `.cppm/generated/cppm_dependencies.cmake` | the include path |
| `cppm_link_dependencies(<target> [deps...])` | link entry point |
| `CPPM_TARGETS_<dep>` | one dependency's targets, for manual wiring |
| `CPPM_DEPENDENCIES` | all dependency names |

Anything prefixed `_cppm_` is private. To take over linking yourself, delete the
`cppm_link_dependencies` call and use `CPPM_TARGETS_<dep>` in your own `target_link_libraries`.

## Requirements

- CMake 3.24 or newer
- Git
- A C++ compiler
- Ninja (optional, but faster)

Run `cppm doctor` to check all of them.

## Build from source

```bash
go build -o cppm ./cmd/cppm
go test ./...
```

No third-party Go dependencies — stdlib only.

## Design notes

[docs/evaluation.md](docs/evaluation.md) — the pre-implementation review, including measurements of
what CMake will and will not tell you about a dependency's targets.

[docs/architecture.md](docs/architecture.md) — schemas, the five-tier target discovery design, and
the reasoning behind each scope boundary.
