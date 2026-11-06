# cppm

**C++ maker.** Start a C++ project, add a dependency, build it — without writing CMake.

```bash
cppm init MyApp
cd MyApp
cppm add github:fmtlib/fmt@11.1.4
cppm build
```

## Install

Linux and macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/OWNER/cppm/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/OWNER/cppm/main/install.ps1 | iex
```

With Go installed:

```sh
go install github.com/OWNER/cppm/cmd/cppm@latest
```

Both installers verify the release checksum, fall back to building from source when no prebuilt
binary matches your platform, and tell you if the install directory is not on your `PATH`. Then run
`cppm doctor` to check your toolchain.

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
| `init` — scaffold a project that builds | Transitive dependency pinning |
| `add` / `remove` — git and local dependencies | Conflict detection |
| `update` — re-resolve refs | Windows / macOS verification |
| `sync` / `apply` — apply cppm.json to CMake | Non-CMake libraries |
| `build` — configure and build via presets | |
| Target discovery: recipes + source scan + ask | |
| `list`, `info`, `doctor`, `clean` | |

## Commands

```
cppm init [<dir>] [--std 20] [--name N] [--lib] [--compiler clang++] [--generator Ninja] [--force]
cppm add <spec> [--target T] [--name N] [--non-interactive] [--no-discover] [--force]
cppm remove <dep>...           alias: rm
cppm update [<dep>...] [--dry-run]
cppm sync [--quiet]            aliases: apply, regen
cppm build [--debug|--release] [--clean] [--jobs N] [--target T] [--verbose]
cppm list [--long]
cppm info <dependency>
cppm doctor
cppm clean [--all]
```

Flags work before or after positional arguments, so `cppm init MyApp --std 17` does what it looks
like.

## Adding dependencies

```bash
cppm add github:fmtlib/fmt@11.1.4
cppm add github:nlohmann/json@v3.11.3
cppm add gitlab:libeigen/eigen@3.4.0
cppm add https://example.com/team/lib.git@main
cppm add local:../MyLibrary
```

A version is an **opaque git ref** — a tag, branch or commit. cppm never parses or compares
versions, so a library that ignores semver is not a problem. Ranges like `^1.2` are deliberately
unsupported. With no `@ref`, cppm lists the repository's tags and asks which to pin.

The ref is resolved to a **commit hash** and recorded in `cppm.lock`, because tags move.

### Finding the right CMake target

This is the part CMake will not help with. Asking a configured project for a dependency's targets
returns only the raw ones — for fmt, `fmt;fmt-header-only` — while the name you are documented to
link, `fmt::fmt`, is an `ALIAS`, and CMake has no API that lists aliases.

So cppm tries, in order:

1. **`--target`** — your explicit choice, always wins.
2. **A curated recipe** — ~25 common libraries, carrying both target names and the cache options
   that keep a dependency from building its own test suite.
3. **A scan of the dependency's own CMake** — correct for 8 of 10 real libraries measured, with
   zero confident errors.
4. **Asking you**, with the candidates listed.

It will **decline rather than guess**. A wrong target name produces a build that configures
successfully and links the wrong thing, which is worse than an error:

```
targets:  unresolved
          cppm could not determine what to link. Set it in cppm.json:
            "mylib": { ..., "targets": ["<real::target>"] }
          then run 'cppm sync'. Or link it yourself in CMakeLists.txt.
```

An inferred name is always labelled as inferred, never presented as fact.

## Changing settings

`cppm.json` is the source of truth, and `cppm init` writes **every setting explicitly, even at its
default**, so you can see what is there to change:

```json
{
  "cxx":       { "standard": 20, "required": true, "extensions": false },
  "cmake":     { "minimum": "3.24", "exportCompileCommands": true,
                 "buildTypes": { "debug": "Debug", "release": "RelWithDebInfo" } },
  "toolchain": { "cxx": "g++", "c": "gcc", "generator": "Ninja" }
}
```

Notable choices, and why they are not what CMake would do on its own:

| Setting | Default | Why |
|---|---|---|
| `cxx.extensions` | `false` | CMake defaults it **on**, which compiles your code as `gnu++NN` and leaves dependencies on mixed dialects |
| `cmake.buildTypes.release` | `RelWithDebInfo` | a release build with no symbols is very hard to debug in the field; set `Release` for a smaller build |
| `cmake.exportCompileCommands` | `true` | clangd and most editors need `compile_commands.json` for completion |
| `toolchain.cxx` | the detected driver | written as `g++` or `clang++`, not the `c++` symlink, so you can see which compiler you are getting |

Edit it and run `cppm sync` (also spelled `apply` or `regen`):

```bash
$ vim cppm.json          # "standard": 17  ->  26
$ cppm sync
  C++ standard 17 -> 26

run 'cppm build' to rebuild with these settings
```

The same applies to the compiler:

```json
"toolchain": { "cxx": "clang++", "generator": "Ninja" }
```

CMake refuses to change compiler in an existing build tree, so `cppm build` notices and reconfigures
from scratch. `cppm doctor` also asks the compiler whether it actually supports the standard you
asked for, rather than letting CMake fail later with an error that never mentions `cppm.json`.

### Who owns which file

| File | Owner | `sync` may rewrite it |
|---|---|---|
| `cppm.json`, `cppm.lock` | you / cppm | — |
| `.cppm/generated/cppm.cmake` | cppm | yes |
| `CMakePresets.json` | cppm | yes, if it carries cppm's `vendor` stamp |
| `CMakeLists.txt` | **you** | never |
| `CMakeUserPresets.json` | **you** | never |

Because `project()` can only be called from `CMakeLists.txt`, cppm cannot apply a name or version
change there. `doctor` reports the drift instead of pretending otherwise.

## Project layout

```
MyApp/
├── cppm.json          what the project wants          (commit this)
├── cppm.lock          what will be fetched             (commit this)
├── CMakeLists.txt     yours — cppm never rewrites it   (commit this)
├── CMakePresets.json  cppm-generated presets           (commit this)
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
| `.cppm/generated/cppm.cmake` | the include path |
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
git clone https://github.com/OWNER/cppm && cd cppm
go build -o cppm ./cmd/cppm
go test ./...
```

Needs Go 1.25+. **No third-party Go dependencies** — stdlib only, so `go build` works offline.

## Contributing

Bug reports, recipes and patches are welcome. See
[docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) — adding a recipe for a library whose CMake targets
cppm cannot discover is the most useful small contribution.

## License

[MIT](LICENSE) © Mehdi Shokohifar

## Design notes

[docs/architecture.md](docs/architecture.md) — how cppm is built and why: file ownership, the
five-tier target discovery design, what CMake will and will not tell you about a dependency's
targets, and the reasoning behind each scope boundary.

[docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) — how to build, test and submit changes.
