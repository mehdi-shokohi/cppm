# cppm

**C++ maker.** Start a C++ project, add a dependency, build it — without writing CMake.

```bash
cppm init MyApp
cd MyApp
cppm add github:fmtlib/fmt@11.1.4
cppm build
cppm test
```

## Demo

<video src="https://github.com/user-attachments/assets/dfe4c00c-6506-40ba-afcf-db7939a135a8" controls width="100%"></video>

[Watch the demo](https://github.com/user-attachments/assets/dfe4c00c-6506-40ba-afcf-db7939a135a8) — scaffold a project, pin a dependency, build and test it.

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

## Why cppm exists

Every other language answers *how do I start?* with one command — `cargo new`, `npm init`,
`go mod init`. C++ answers it with a reading list, and that first hour is where a lot of people
quietly stop. cppm is the short path: a project that builds, dependencies pinned to commits, the
CMake wiring worked out for you, and the warning, hardening and sanitizer tooling that already
ships with your toolchain switched on instead of waiting for you to know it exists.

It is not a new build system and not a new registry, because neither of those is the part that
was missing.

The longer version — the gap, the safety argument in an era that asks about it, and what cppm
deliberately refuses to do: **[docs/why-cppm.md](docs/why-cppm.md)**.

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
| `init` — scaffold a project that builds | Debug standard library |
| `add` / `remove` — git and local dependencies | Transitive dependency pinning |
| `update` — re-resolve refs | Conflict detection |
| `sync` / `apply` — apply cppm.json to CMake | Windows / macOS verification |
| `build` — configure and build via presets | Non-CMake libraries |
| `test` — build and run via CTest | |
| Warnings and release hardening, on by default | Static analysis (clang-tidy) |
| Sanitizers: asan, ubsan, asan+ubsan, tsan | Generated CI stages |
| Valgrind: memcheck, helgrind, drd, massif, cachegrind | |
| Dev-only dependencies, kept out of release builds | |
| Target discovery: recipes + source scan + ask | |
| `list`, `info`, `doctor`, `clean` | |

## Commands

```
cppm init [<dir>] [--std 20] [--name N] [--lib] [--with-tests] [--compiler clang++]
          [--generator Ninja] [--non-interactive] [--force]
cppm add <spec> [--target T] [--name N] [--dev] [--non-interactive] [--no-discover] [--force]
cppm install [--no-configure] [--release] [--non-interactive] [--no-discover]
                               aliases: fetch, bootstrap
cppm remove <dep>...           alias: rm
cppm sources [add|remove] <pattern>... [--target T]
                               alias: src
cppm update [<dep>...] [--dry-run]
cppm sync [--quiet]            aliases: apply, regen
cppm build [--debug|--release] [--clean] [--jobs N] [--target T] [--verbose]
                               [--warnings off|default|strict|error]
                               [--sanitize asan|ubsan|asan+ubsan|tsan]
cppm test [--debug|--release] [--filter RE] [--jobs N] [--verbose] [--list] [--no-build] [--fail-fast]
                               [--warnings off|default|strict|error]
                               [--sanitize asan|ubsan|asan+ubsan|tsan]
                               [--memcheck|--helgrind|--drd|--massif|--cachegrind]
                               [--gen-suppressions]
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

### Libraries that use git submodules

They work, and you do not have to do anything. CMake's `FetchContent` initialises a dependency's
submodules recursively by itself, and the pin on the superproject's commit pins the submodules
too — their commits are recorded inside that commit's tree.

`cppm add` also fetches them into its own cache while inspecting the library, for one reason: so it
can tell you *now* if one is unreachable. Otherwise the first sign is a wall of git and CMake output
ending in `Failed to update submodules`, after the dependency is already added and the clone
downloaded:

```
  2 git submodule(s) declared

  warning: 1 of drogon's submodules could not be fetched:
             third_party/eric-hpack-core
               https://gitlab.com/joe1231231218/eric-hpack-core.git
           cppm added the dependency anyway, but 'cppm build' fetches the same
           submodules through CMake, so expect it to fail the same way.
```

A submodule host that is blocked or rate-limiting you is the usual cause, and a mirror fixes it for
both cppm and CMake, because both just run `git`:

```bash
git config --global url.<reachable-mirror>.insteadOf https://gitlab.com/owner/repo.git
```

One unreachable submodule does not cost you the others — `git submodule update` stops at the first
failure, so cppm retries the rest one at a time.

Targets defined *inside* a submodule are never offered as the library's own. A submodule is another
package vendored in, so its targets are not what you link this one for.

`cppm add --no-submodules` skips the fetch, which is worth it for a library with large submodules
you know you do not need inspected.

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

### Asked, not guessed

`cppm init` with no flags, in a terminal, asks before it writes:

```
project name [MyApp]:
kind [1]:                      1) executable   2) library
scaffold tests [Y/n]:
C++ standard [20]:
compiler [1]:                  1) g++   2) clang++
create it [Y/n]:
```

Enter takes the bracketed default throughout, which is what a bare `cppm init` always produced —
with one exception: for a library, tests default to **on**. A test target can link a library, so
that scaffold exercises the real code from the first commit; an executable cannot be linked into
another target, so there the default stays off rather than handing you a test that can only check
itself.

The recap prints the flags that reproduce your answers, because the second project should not have
to be asked:

```
the same project without the questions:
    cppm init MyApp --lib --with-tests --std 20
```

The questions are skipped entirely when any flag is passed, when `--non-interactive` is given, or
when the output is not a terminal — so a script or a CI job is never asked anything, and `cppm init`
in a pipeline behaves exactly as it did before.

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

## Warnings and hardening

Most C++ projects ship with the warnings effectively off, because nobody remembers the flags. A new
cppm project has them on:

```json
"safety": { "warnings": "default", "hardening": true }
```

| Level | GCC / Clang | MSVC |
|---|---|---|
| `off` | nothing — the compiler's defaults | nothing |
| `default` | `-Wall -Wextra` | `/W3` |
| `strict` | adds `-Wpedantic -Wshadow -Wconversion -Wold-style-cast -Wnon-virtual-dtor -Wcast-align -Wunused -Woverloaded-virtual -Wdouble-promotion -Wformat=2` | `/W4 /permissive-` |
| `error` | `strict` plus `-Werror` | `/WX` |

Edit `cppm.json` and run `cppm sync` to keep a level, or try one out for a single build:

```bash
$ cppm build --warnings=strict
  warnings: strict (from --warnings, not saved to cppm.json)
```

`--warnings` is a one-off on purpose. The level in `cppm.json` is the one CI will use, and trying a
stricter one should not produce a commit. `cppm test --warnings=error` works the same way, so you
can build the tests under `-Werror` without changing the project.

**`error` is meant for CI, not for your laptop.** A new compiler release can introduce a warning
your code did not change to earn, and `-Werror` turns that into a project that will not build.

### The flags go on your targets only

They are applied with `target_compile_options(<your target> PRIVATE ...)`, never with
`add_compile_options()`. The difference is the whole feature: `add_compile_options()` applies to the
current directory *and every directory added below it*, and `FetchContent` brings each dependency in
as a subdirectory — so `-Werror` would be enforced on `fmt` and `spdlog`, where a warning is not
your bug and an upstream release can stop your build without you touching a line. `PRIVATE` also
keeps the flags from being inherited by anything that links a library from your project.

Only the targets named in `cppm.json`'s `"targets"` are covered. A target you add to
`CMakeLists.txt` by hand gets nothing — add it to `cppm.json`, or set the options on it yourself.

### Hardening

Separate from warnings, and aimed at release builds, where it costs almost nothing:

```
-D_GLIBCXX_ASSERTIONS        bounds-check std containers (ABI-compatible, so it is safe here)
-U_FORTIFY_SOURCE
-D_FORTIFY_SOURCE=2          checked libc calls -- optimised configurations only
-fstack-protector-strong     stack smashing detection
-fstack-clash-protection
-fcf-protection              control-flow integrity
-Wl,-z,relro -Wl,-z,now      linker hardening
```

On MSVC: `/sdl /guard:cf` and `/GUARD:CF` at link time, which only works as a pair.

Three things cppm handles that are easy to get wrong by hand:

- **`_FORTIFY_SOURCE` does nothing at `-O0`.** It is implemented in glibc's headers through
  `__builtin_object_size`, which computes nothing until the optimiser runs, so it is applied to
  `Release`, `RelWithDebInfo` and `MinSizeRel` only.
- **The `-U` has to come first.** Ubuntu, Fedora and SUSE already define `_FORTIFY_SOURCE` in their
  GCC specs, and a bare `-D` warns about a redefined macro on every file you compile.
- **Half of these are a hard error off x86.** `-fstack-clash-protection` is x86/s390x only,
  `-fcf-protection` is x86 only, and Apple's linker rejects `-z` outright. Each flag is put to the
  compiler with `check_cxx_compiler_flag` before it is used, and the answer is cached in
  `CMakeCache.txt`, so it costs one configure rather than one build.

`_GLIBCXX_DEBUG` — the heavier debug standard library — is deliberately **not** here. It changes the
layout of `std::` types, so every dependency has to be rebuilt with it to match. That is a separate
feature, and one cppm is unusually well placed to offer, since it builds dependencies from source in
the same tree.

To turn all of it off, for adopting cppm in a codebase that cannot yet survive `-Wall`:

```json
"safety": { "warnings": "off", "hardening": false }
```

That emits nothing at all — not even the compiler probes. `cppm doctor` reports the level either
way, and warns on both `off` (the compiler is telling you nothing) and `error` (a new compiler
release can stop the build).

## Finding memory bugs and data races

Two mechanisms, and they are not alternatives — they find overlapping but
different things, at very different costs.

| | Sanitizers (`--sanitize=`) | Valgrind (`--memcheck`, …) |
|---|---|---|
| Needs recompiling | yes, into its own build tree | **no** — runs the binary you have |
| Speed | 5–15× slower | 20–100× slower |
| False positives | few | more, especially with custom synchronisation |
| Uninitialised-memory reads | not covered (that is MSan, see below) | **yes** |
| Lock-order inversion | partial | **yes, thorough** |
| Works on a release binary | no | **yes** |
| Platforms | Linux, macOS, Windows | Linux only in practice |

The short version: **use the sanitizers routinely, and valgrind when you cannot
recompile or when you need what only it finds.**

### Sanitizers

Each gets its own build tree and its own preset, so nothing is shared with your
ordinary `debug` build:

```bash
cppm build --sanitize=asan+ubsan     # the usual pairing
cppm test  --sanitize=asan+ubsan
cppm test  --sanitize=tsan           # data races and lock-order inversions
```

| Name | Finds |
|---|---|
| `asan` | heap and stack buffer overflow, use-after-free, double free, memory leaks |
| `ubsan` | signed overflow, bad shifts, misaligned access, bad casts, null dereference |
| `asan+ubsan` | both — what you want most of the time |
| `tsan` | data races and lock-order inversions |

`cppm test --sanitize=<name>` also sets the runtime options, which matter as
much as the compile flags:

```
ASAN_OPTIONS=abort_on_error=1:detect_stack_use_after_return=1
UBSAN_OPTIONS=print_stacktrace=1:halt_on_error=1
TSAN_OPTIONS=second_deadlock_stack=1:halt_on_error=1
```

Without those, UBSan in particular prints the error and carries on — your suite
passes, CI goes green, and the undefined behaviour is still there. Every
sanitizer cppm configures halts on the first error.

### Valgrind

No recompiling: valgrind runs the binary you already built, under emulation.
Wired through CTest's own memcheck support, so `ctest -T memcheck` works by hand
too.

```bash
cppm test --memcheck      # leaks, invalid reads/writes, uninitialised values
cppm test --helgrind      # data races AND lock-order violations
cppm test --drd           # lighter thread checker, better with many threads
cppm test --massif        # heap profiler: what allocated memory, and when
cppm test --cachegrind    # cache and branch-prediction profiler
```

**`--memcheck` is the heap and memory checker.** It is the one to reach for when
something is corrupting memory or growing without bound. It reports:

- **Memory leaks** — memory allocated and never freed. cppm runs it with
  `--leak-check=full --show-leak-kinds=all`, which separates
  *definitely lost* (nothing points at it any more — a real leak),
  *indirectly lost* (reachable only from something already lost, such as the
  children of a leaked tree node), *possibly lost* (only an interior pointer
  remains) and *still reachable* (never freed, but you could have — typically a
  cache or a global, and usually harmless). Chase *definitely lost* first;
  *still reachable* is mostly noise.
- **Invalid reads and writes** — reading or writing past the end of a heap
  block, or through a pointer to memory already freed.
- **Uninitialised values** — using memory that was never written. cppm passes
  `--track-origins=yes`, which reports *where the uninitialised value came
  from*, not just where it was used. That roughly halves the speed and is worth
  it: the use site alone is rarely enough to find the bug. **This is the one
  thing neither ASan nor UBSan finds** — the sanitizer for it is MSan, which
  cppm does not offer (see below).
- **Double frees and mismatched frees** — `free()` on `new`, `delete` on
  `malloc`.

**`--massif` is the heap profiler.** It answers "why is this using 2 GB" rather
than "is this leaking": it samples the heap over time and attributes every byte
to the call stack that allocated it. cppm runs it with `--stacks=yes`, so stack
growth is accounted for too. It writes `massif.out.<pid>` into the build tree;
read it with `ms_print massif.out.<pid>`.

**`--helgrind` and `--drd` are the thread checkers.** Helgrind is the thorough
one for lock ordering — it will tell you that thread A takes mutex X then Y
while thread B takes Y then X, which is a deadlock waiting for the right
timing, even if it has never actually deadlocked. Use `--drd` instead when
helgrind is too slow or too noisy with many threads.

**Which race checker?** `--sanitize=tsan` routinely: far faster and with fewer
false positives. `--helgrind` when you cannot recompile, when you need to check
a release binary, or when you specifically suspect a lock-ordering deadlock.

### Suppressions

The first `--memcheck` run on any real project reports errors inside glibc, GTK,
OpenSSL or CUDA that are not yours to fix. `cppm init` scaffolds a commented
`valgrind.supp` and wires it in; generate entries for what you actually see
rather than writing valgrind's stack-frame syntax by hand:

```bash
cppm test --memcheck --gen-suppressions
```

Paste the blocks it prints into `valgrind.supp` and commit it. Prefer the
narrowest entry that works — a suppression matching too much will one day hide a
real bug of yours.

### The rules cppm encodes so you do not have to learn them

These are the mistakes that make a safety tool report nothing, or report
nonsense, while looking like it worked.

- **A sanitizer must be in the link flags as well as the compile flags.**
  Compiled in but not linked fails with undefined references to `__asan_*`.
  cppm sets `CMAKE_EXE_LINKER_FLAGS`, `CMAKE_SHARED_LINKER_FLAGS` and
  `CMAKE_MODULE_LINKER_FLAGS` alongside `CMAKE_CXX_FLAGS`.
- **ASan and TSan cannot be combined.** Both intercept allocation and relocate
  the heap, so a binary carrying both runtimes does not start. There is no cppm
  name meaning both, and asking for one gets a sentence rather than a link
  error.
- **TSan needs the whole program instrumented.** Partially instrumented code
  produces races whose synchronisation TSan cannot see — false positives that
  waste a day. Because cppm builds every dependency from source in the same
  tree, its sanitizer flags reach all of it. This is the opposite of what cppm
  does with warnings, deliberately: see [the note on warnings](#the-flags-go-on-your-targets-only).
- **A dependency can overrule those flags, and one does.** duckdb's
  `ENABLE_SANITIZER` and `ENABLE_UBSAN` both default ON and append
  `-fsanitize=address` to `CMAKE_CXX_FLAGS_DEBUG`, so `--sanitize=tsan` against
  a default duckdb would put ASan and TSan in one translation unit. cppm scans
  your dependencies' CMake for this, refuses the build, and names the option to
  set OFF in `cppm.json`.
- **Never run a sanitizer-instrumented binary under valgrind.** They intercept
  the same allocations and each reports the other's bookkeeping as an error, so
  the output means nothing. cppm refuses the combination from the CLI, and the
  generated CMake disables memcheck in an instrumented build tree so a bare
  `ctest -T memcheck` cannot do it either.
- **`--error-exitcode=1` is what makes valgrind able to fail CI.** Without it
  valgrind reports the error, exits 0, and CTest records a pass: a green
  pipeline with a use-after-free in it. Every error-finding tool cppm runs
  carries it.
- **TSan needs address-space randomisation off.** It maps its shadow memory at
  fixed addresses, and a kernel with high-entropy ASLR — the default on current
  Ubuntu, Fedora and Arch — can place the binary where that will not fit. TSan
  then dies before `main()` with `unexpected memory mapping`, which names
  neither the cause nor the fix. `cppm test --sanitize=tsan` launches the run
  under `setarch -R` so this does not happen.
- **A sanitizer build sets no `CMAKE_BUILD_TYPE`.** The sanitizer flags carry
  `-g -O1` themselves — Google's recommendation for ASan: readable stack traces,
  several times faster than `-O0`. A build type's own `-O0` or `-O2` comes later
  on the command line and would win. No build type also means no `-DNDEBUG`, so
  your `assert()`s stay live, and it keeps `_FORTIFY_SOURCE` out of the build —
  fortified libc calls hide the very overflows ASan is there to catch.
- **`compute-sanitizer` is not valgrind.** CMake's own CTest module searches for
  a memory checker over a list that includes NVIDIA's `compute-sanitizer`, so on
  a machine with the CUDA toolkit a naive setup ends up running that with
  valgrind's options. cppm pins the tool for each run instead of trusting the
  build tree.

### What cppm deliberately does not offer

**MemorySanitizer.** It is the sanitizer for uninitialised reads, and it
requires *every* dependency to be instrumented, including libc++ — which in
practice means building a custom standard library. `cppm test --memcheck` finds
the same class of bug with no such cost, which is why that is the answer here.

**Valgrind on macOS and Windows.** Valgrind does not run on Windows and is
broken on Apple Silicon. `cppm doctor` says so rather than only reporting it
missing, and points at `--sanitize=asan+ubsan` and `--sanitize=tsan`, which are
supported there and cover most of the same ground.

All of the above is verified on Linux with GCC 13, Clang and valgrind 3.22. The
MSVC paths — `/fsanitize=address`, and the refusal for the sanitizers MSVC does
not implement — are covered by tests against the generated CMake only; there is
no Windows CI yet.

### Where this belongs in CI

Cost rises steeply down this list, so they do not all belong on every push:

```
warnings          no runtime cost          every push
hardening         negligible               every push
asan+ubsan        5-15x slower             every push, if the suite is quick
tsan              5-15x slower             every push for threaded code
memcheck          20-50x slower            nightly
helgrind          20-100x slower           nightly, or when hunting a deadlock
massif/cachegrind profiling, not checking  on demand
```

## Changing settings

`cppm.json` is the source of truth, and `cppm init` writes **every setting explicitly, even at its
default**, so you can see what is there to change:

```json
{
  "cxx":       { "standard": 20, "required": true, "extensions": false },
  "cmake":     { "minimum": "3.24", "exportCompileCommands": true,
                 "buildTypes": { "debug": "Debug", "release": "RelWithDebInfo" } },
  "toolchain": { "cxx": "g++", "c": "gcc", "generator": "Ninja" },
  "safety":    { "warnings": "default", "hardening": true }
}
```

Notable choices, and why they are not what CMake would do on its own:

| Setting | Default | Why |
|---|---|---|
| `cxx.extensions` | `false` | CMake defaults it **on**, which compiles your code as `gnu++NN` and leaves dependencies on mixed dialects |
| `cmake.buildTypes.release` | `RelWithDebInfo` | a release build with no symbols is very hard to debug in the field; set `Release` for a smaller build |
| `cmake.exportCompileCommands` | `true` | clangd and most editors need `compile_commands.json` for completion |
| `toolchain.cxx` | the detected driver | written as `g++` or `clang++`, not the `c++` symlink, so you can see which compiler you are getting |
| `safety.warnings` | `default` | a tool that generates the build has no excuse for generating one with the warnings off. `-Wall -Wextra` cannot fail a build on their own |
| `safety.hardening` | `true` | every flag is free or close to it in an optimised build, and the ones that are not portable are put to the compiler before they are used |

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
├── cppm.json          what the project wants           (commit this)
├── cppm.lock          what will be fetched             (commit this)
├── CMakeLists.txt     yours — cppm never rewrites it   (commit this)
├── CMakePresets.json  cppm-generated presets           (commit this)
├── valgrind.supp      valgrind suppressions            (commit this)
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

**Headers are not sources.** The directory form takes `.cpp`, `.cc`, `.cxx` and `.c++` only. What
makes `#include "myapp/net/socket.hpp"` resolve is the include *path*, not a pattern — see
[Headers](#headers) below, where `init` has already done it for `include/`. An explicit pattern
does match headers:

```bash
cppm sources add "include/**/*.hpp"   # IDE project trees only; changes no #include
```

CMake marks a header `HEADER_FILE_ONLY` by extension, so nothing is compiled twice and nothing
breaks — it only makes the files appear in IDE project trees. Skip it unless you want that.

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

This line is the one thing `cppm sources` cannot do for you. CMake has no separate "add a header
folder" directive — `target_include_directories()` *is* that directive, and it takes a list, so a
new header root is one word appended to a line already in your file. It is a one-time edit per
folder, not per file, which is why cppm leaves it alone.

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
- Valgrind (optional, Linux only — for `cppm test --memcheck` and `--helgrind`)

The sanitizers need no extra tool: `--sanitize=asan+ubsan` and `--sanitize=tsan`
work with any reasonably current GCC or Clang, and `--sanitize=asan` works with
MSVC too.

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
