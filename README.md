# cppm

**C++ maker.** Start a C++ project, add a dependency, build it — without writing CMake.

```bash
cppm init MyApp
cd MyApp
cppm add github:fmtlib/fmt@11.1.4
cppm build
cppm test
```

## Install

Linux and macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/mehdi-shokohi/cppm/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/mehdi-shokohi/cppm/main/install.ps1 | iex
```

With Go installed:

```sh
go install github.com/mehdi-shokohi/cppm/cmd/cppm@latest
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
| `init` — scaffold a project that builds | Warnings, hardening, sanitizers, valgrind |
| `add` / `remove` — git and local dependencies | Transitive dependency pinning |
| `update` — re-resolve refs | Conflict detection |
| `sync` / `apply` — apply cppm.json to CMake | Windows / macOS verification |
| `build` — configure and build via presets | Non-CMake libraries |
| `test` — build and run via CTest | |
| Dev-only dependencies, kept out of release builds | |
| Target discovery: recipes + source scan + ask | |
| `list`, `info`, `doctor`, `clean` | |

## Commands

```
cppm init [<dir>] [--std 20] [--name N] [--lib] [--with-tests] [--compiler clang++]
          [--generator Ninja] [--force]
cppm add <spec> [--target T] [--name N] [--dev] [--non-interactive] [--no-discover] [--force]
cppm install [--no-configure] [--release] [--non-interactive] [--no-discover]
                               aliases: fetch, bootstrap
cppm remove <dep>...           alias: rm
cppm sources [add|remove] <pattern>... [--target T]
                               alias: src
cppm update [<dep>...] [--dry-run]
cppm sync [--quiet]            aliases: apply, regen
cppm build [--debug|--release] [--clean] [--jobs N] [--target T] [--verbose]
cppm test [--debug|--release] [--filter RE] [--jobs N] [--verbose] [--list] [--no-build] [--fail-fast]
cppm list [--long]
cppm info <dependency>
cppm doctor
cppm clean [--all]
```

Flags work before or after positional arguments, so `cppm init MyApp --std 17` does what it looks
like.

## Cloning a project that uses cppm

```bash
git clone https://example.com/team/app
cd app
cppm install
```

`install` needs nothing but `cppm.json`. It resolves any dependency `cppm.lock` does not already
pin, regenerates `.cppm/generated/cppm.cmake` and `CMakePresets.json`, and configures the build
tree — which is the step that actually downloads the dependencies. Then `cppm build`.

It never moves a pin. A dependency already in `cppm.lock` is fetched at exactly that commit, even
if `cppm.json` has since been changed to ask for a different ref; `cppm doctor` reports that
disagreement and only `cppm update` acts on it. If the lockfile is missing or incomplete, the gaps
are resolved and written, and the result is worth committing.

`cppm install --no-configure` stops after writing the generated files, before any network work.

This is not `cmake --install`: cppm does not copy build output into a prefix.

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

### Dev-only dependencies

Catch2, googletest, doctest and benchmark exist to build the tests, not the product. `--dev` keeps
them out of everything that ships:

```bash
cppm add github:catchorg/Catch2@v3.7.1 --dev
```

A dev dependency is still fetched, configured and verified — `CPPM_TARGETS_<dep>` works exactly as
it does for any other. It is simply left out of `CPPM_DEPENDENCIES`, which is what
`cppm_link_dependencies(<target>)` links when given no list. Instead it is linked into the targets
marked `"test": true` in `cppm.json`, so adding a framework takes effect without touching
`CMakeLists.txt`.

Listing a dev dependency on a target that is not a test is a `cppm.json` error rather than a
surprise in a release binary:

```
target "MyApp" depends on "Catch2", which is marked "dev": true in cppm.json.
a dev dependency may only be linked into a test target. Either add "test": true to
targets.MyApp, or remove "dev": true from dependencies.Catch2 to link it everywhere
```

## Tests

```bash
cppm init MyApp --with-tests     # a tests/ directory and a first test
cd MyApp
cppm test
```

`cppm test` is a wrapper over CMake presets, like `build`:

```
cmake --preset debug
cmake --build --preset debug
ctest --preset debug
```

It builds first on purpose — a test run against a stale build tree reports on code that is no
longer there. Pass `--no-build` for the fastest loop when you know the tree is current.

```bash
cppm test --release
cppm test --filter parser        # ctest -R
cppm test --verbose              # output from passing tests too
cppm test --list                 # what would run
```

Failing tests always print their own output, because the test presets set `outputOnFailure`.

The test scaffolded by `--with-tests` uses **no framework**: `init` has to work offline and before
any dependency exists, so the first suite compiles and runs against nothing but the standard
library. Swap in a real one when you want it — the scaffolded file says how.

Anything registered with `add_test()` in your `CMakeLists.txt` is run, whether cppm scaffolded it
or you wrote it. Marking a target `"test": true` in `cppm.json` is what routes dev dependencies to
it; it is a declaration, never inferred from a name like `foo_tests`.

Two CMake facts cppm handles so you do not have to:

- **`include(CTest)` is emitted after the dependencies.** It defines `BUILD_TESTING` and defaults
  it to `ON`, and a great many libraries build their own test suites when that is set. cppm also
  forces `BUILD_TESTING` off across the dependency section and restores it afterwards.
- **`include(CTest)` is guarded by `PROJECT_IS_TOP_LEVEL`**, so a cppm project consumed through
  `add_subdirectory` does not switch testing on in its parent.

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
├── tests/             with --with-tests                (commit this)
├── .cppm/             generated files                  (git-ignored)
└── build/             build output                     (git-ignored)
```

## Adding files and folders

Two ways, and the difference is who keeps the list.

### cppm sources — let cppm track a folder

```bash
cppm sources add src/net            # every C++ source under src/net, recursively
cppm sources add "src/**/*.cpp"     # the same, for the whole tree
cppm sources add "src/codec/*.cpp"  # one directory, not its subdirectories
cppm sources                        # the patterns, and what they match right now
cppm sources remove src/net
```

Quote a pattern containing `*` so the shell does not expand it first. `*`, `?` and `[class]` match
within one path segment; `**` spans directories. **Prefer the plain directory form** — a file
added to `src/net` tomorrow needs no new pattern.

The patterns live in `cppm.json`:

```json
"targets": {
  "MyApp": {
    "kind": "executable",
    "sources": ["src/main.cpp"],
    "sourceGlobs": ["src/net"]
  }
}
```

`sources` is what *your* `CMakeLists.txt` names. `sourceGlobs` is what cppm attaches on top, from
`.cppm/generated/cppm.cmake` — so **`CMakeLists.txt` is still never written to after `init`**. The
generated file calls `target_sources()` through `cmake_language(DEFER)`, because the include runs
before your `add_executable()` does.

The patterns are expanded by `cppm sync`, not globbed by CMake. A `file(GLOB)` at configure time
makes the set of translation units depend on whatever is on disk at that moment and shows up in no
diff. The cost of expanding ahead of time is that a **newly created file is compiled only after a
sync** — and `cppm build` syncs first, so in practice you just build.

Some directories are never searched, or `**/*.cpp` would compile every dependency into your own
target: anything beginning with a dot (so `.cppm/`, which holds the dependency sources), `build/`,
and any directory containing a `CMakeCache.txt`.

Files already in `sources` are skipped, since `CMakeLists.txt` names those itself.

### By hand in CMakeLists.txt

Nothing stops you doing it yourself, and for a handful of files it is clearer. Note that editing
`sources` in `cppm.json` is *not* the way: that list is read by `init` only, so changing it later
has no effect on the build.

List the file in the target:

```cmake
add_executable(MyApp
    src/main.cpp
    src/net/socket.cpp
    src/net/resolver.cpp
)
```

Nothing to sync — the file is in your target the moment CMake next configures, and `cppm build`
configures. A list you maintain by hand stays the clearest record of what a target compiles; the
`sourceGlobs` above exist for the trees where that stops being true.

### Headers

`init` creates `include/` and generates:

```cmake
target_include_directories(MyApp PRIVATE include)
```

So headers under `include/` are found by their path below it: `include/myapp/net/socket.hpp` is
`#include "myapp/net/socket.hpp"`. The extra directory level is what keeps your headers from
colliding with a dependency's. For headers private to the implementation, add `src` as well:

```cmake
target_include_directories(MyApp PRIVATE include src)
```

A library whose headers its *consumers* need must say `PUBLIC` rather than `PRIVATE`; `init` writes
`PRIVATE` because the scaffold has no consumers yet.

### A subfolder with its own CMakeLists.txt

Works normally:

```cmake
include("${CMAKE_CURRENT_SOURCE_DIR}/.cppm/generated/cppm.cmake")   # keep this above
add_subdirectory(src/engine)
```

`cppm_link_dependencies()` is a CMake function and `CPPM_TARGETS_<dep>` is an ordinary variable, so
both are visible inside the subdirectory — provided the generated include stays above the
`add_subdirectory()` call, as `init` wrote it.

### A new target you add by hand

Two things cppm cannot infer from a target that exists only in `CMakeLists.txt`:

- **Dev dependencies.** `cppm_link_dependencies` adds them only for targets declared
  `"test": true` in `cppm.json`. Either add the target there and run `cppm sync`, or name the
  dependencies explicitly: `cppm_link_dependencies(my_tests Catch2)`.
- **`cppm test --filter`.** It matches CTest names, so a hand-written test target needs its own
  `add_test(NAME my_tests COMMAND my_tests)`.

## The generated CMake interface

`init` writes an `include()` of cppm's generated file into your `CMakeLists.txt`. That file exposes
exactly four public names, which is a compatibility surface cppm will not change:

| Name | Purpose |
|---|---|
| `.cppm/generated/cppm.cmake` | the include path |
| `cppm_link_dependencies(<target> [deps...])` | link entry point |
| `CPPM_TARGETS_<dep>` | one dependency's targets, for manual wiring |
| `CPPM_DEPENDENCIES` | the dependency names that ship (dev dependencies excluded) |

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
git clone https://github.com/mehdi-shokohi/cppm && cd cppm
go build -o cppm ./cmd/cppm
go test ./...
```

Needs Go 1.23+. **No third-party Go dependencies** — stdlib only, so `go build` works offline.

## AI assistant skill

[`skills/cppm/`](skills/cppm/) is a skill that teaches an AI coding assistant to drive the cppm CLI
correctly — the full command reference, and the mistakes it would otherwise make, such as
hand-writing `FetchContent` instead of running `cppm add`. Install it with:

```sh
mkdir -p ~/.claude/skills && cp -r skills/cppm ~/.claude/skills/
```

See [skills/README.md](skills/README.md) for other assistants and per-project installs.

## Contributing

Bug reports, recipes and patches are welcome. See
[docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) — adding a recipe for a library whose CMake targets
cppm cannot discover is the most useful small contribution.

## License

[MIT](LICENSE) © Mehdi Shokohifar

## Roadmap

[docs/ROADMAP.md](docs/ROADMAP.md). v0.3 is about making **your** C++ project safer, with the
configuration already written: strict warnings and hardening flags, a debug standard library,
sanitizer presets (ASan, UBSan, TSan), valgrind/helgrind/DRD through CTest's memcheck, and
clang-tidy — reached through `cppm test` and `cppm build --sanitize=...`.

## Design notes

[docs/architecture.md](docs/architecture.md) — how cppm is built and why: file ownership, the
five-tier target discovery design, what CMake will and will not tell you about a dependency's
targets, and the reasoning behind each scope boundary.

[docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) — how to build, test and submit changes.
