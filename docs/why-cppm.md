# Why cppm exists

What was missing, why it is a small gap rather than a large one, and why a C++ tool written in
2026 has to have an answer about safety. For using cppm see the [README](../README.md); for how it
is built, [architecture.md](architecture.md).

---

## The first hour

Every other language I reach for answers *how do I start?* with one command. `cargo new`,
`npm init`, `go mod init`. You get something that builds, a manifest, a lockfile, and a place to
put your first line of code. You are learning the language within a minute.

C++ answers the same question with a reading list. Pick a build system and learn its syntax —
which is its own language, with its own scoping rules and a decade of accumulated advice, much of
it now wrong. Pick a package manager and learn its model, its registry, its toolchain files. Then
write thirty lines of boilerplate before your first `main()`.

None of that is the thing you sat down to do, and it is where a lot of people trying C++ stop. Not
at templates, not at move semantics, not at undefined behaviour. At `CMakeLists.txt`.

## The gap is narrower than it looks

It would be easy to read that as "C++ needs a new build system" or "C++ needs a better package
manager". cppm is built on the belief that neither is true.

- **CMake** builds anything, on every platform, and the whole ecosystem speaks it. It is not going
  to be replaced.
- **vcpkg** and **Conan** are real package managers, with registries, maintainers and version
  solving. If you need those, use them.

What is missing is the short path between them. The gap is not capability — it is the distance from
zero to a building project, and the fact that crossing it requires knowing things that have nothing
to do with the program you want to write. That gap needs three things, and none of them is a build
system or a registry.

### 1. A scaffold that actually builds

`cppm init` writes `CMakeLists.txt`, a `main.cpp`, a manifest and presets, and the result
configures, builds and runs tests. Every setting is written out at its default value, because a
setting you cannot see is a setting you cannot change. Nothing important hides in a tool's
defaults.

### 2. Dependency wiring you do not have to understand

This is the genuinely hard part, and the one easy to underestimate.

When you add `fmt`, the string you need in `target_link_libraries` is `fmt::fmt`. Ask a configured
CMake project what targets a dependency defines and you get `fmt;fmt-header-only` — the raw
targets. `fmt::fmt`, the name every piece of documentation tells you to link, is an `ALIAS`, and
CMake exposes no property and no command that enumerates aliases. The one name you need is the one
name CMake will not tell you.

It will, however, *confirm* a name you already have: `if(TARGET fmt::fmt)` works reliably. So
discovery is: generate candidates, rank them, let CMake verify — backed by a curated database for
the libraries people actually use. When ranking is not confident, cppm stops and asks, or names the
exact line to write.

That last part is a design rule, not a limitation. A wrong link target that configures, links and
is silently wrong is the worst thing cppm can produce, so declining beats guessing. See
[architecture.md](architecture.md) §3 for the five tiers and the confidence gate.

### 3. A hand-off with nothing to migrate off

`CMakeLists.txt` is yours from `init` onward and cppm never writes to it again. cppm owns
`.cppm/generated/cppm.cmake` and `CMakePresets.json`, and nothing else.

When a project outgrows generated wiring — and a non-trivial one will — you open CMake and edit it
like everyone else. There is no lock-in because there is nothing to leave. cppm never owns the
build; it generates CMake and shells out to `cmake`, `ninja` and `git`.

## Safety, in an era that asks about it

There is a reason to build this now rather than five years ago, and it is Rust.

Rust made *safe by default* the standard a language gets judged against, and that standard is
fair. C++ will not win that argument with a borrow checker it does not have, and cppm does not
contain one.

But a real part of the distance is not about the language at all. It is about tooling that already
exists, already works, and is **off unless you know to ask for it**:

| Already in your toolchain | On by default? |
|---|---|
| `-Wall -Wextra`, and the stricter set past it | no |
| `_FORTIFY_SOURCE`, `-fstack-protector-strong`, `-fcf-protection`, RELRO | no |
| AddressSanitizer, UBSan, ThreadSanitizer | no — and each wants its own build tree |
| valgrind `memcheck`, `helgrind`, `drd` | no — and the CTest wiring is unobvious |

Each is a line of CMake that an experienced C++ developer knows by heart and a newcomer has never
heard of. The flags are free and the information is public; the default experience of the language
is to have none of it.

It is worse than merely "off", because several of them are easy to switch on *incorrectly*, in ways
that fail silently and leave you believing you are covered:

- **`_FORTIFY_SOURCE` does nothing at `-O0`.** It works through glibc's headers and
  `__builtin_object_size`, which computes nothing until the optimiser runs. In a debug build it buys
  exactly nothing, so cppm applies it to optimised configurations only.

- **Warning flags leak downward.** `add_compile_options()` applies to the current directory *and
  every directory added below it*, and `FetchContent` brings each dependency in as a subdirectory.
  The obvious way to turn on `-Werror` enforces it on fmt and spdlog, where a warning is not your
  bug and an upstream release can stop your build without you touching a line. cppm puts them on
  your own targets, `PRIVATE`.

- **A sanitizer build with a build type finds less.** `CMAKE_BUILD_TYPE` brings its own `-O0` or
  `-O2`, overriding the `-O1` the sanitizer asked for — and `-O2` switches `_FORTIFY_SOURCE` on,
  which hides the overflows ASan exists to find. cppm's sanitizer presets carry a name, not flags,
  and no build type; the generated CMake picks the flags, because only CMake knows whether the
  compiler is MSVC.

- **`ctest -T memcheck` can pass without running anything.** It is a dashboard action and reads its
  configuration from the *current* directory, not the preset's binary directory. Pointed at the
  wrong one it prints `No tests were found!!!` and **exits 0** — a green stage that checked nothing.
  This shipped in cppm itself: correct generated CMake, passing unit tests, and a valgrind feature
  that did nothing. Only an end-to-end test driving the real binary caught it.

That list is the argument for encoding this in a tool. Not that C++ developers are careless — the
opposite. The knowledge is specialist, scattered, and each piece of it fails quietly.

So a new cppm project has warnings and release hardening on, and the rest is one flag:

```bash
cppm build                         # -Wall -Wextra on your targets, hardening in release
cppm test --sanitize=asan+ubsan    # own build tree, own preset, correct -O level
cppm test --sanitize=tsan          # data races, lock-order inversions
cppm test --memcheck               # valgrind, and it proves it ran
```

Each hardening flag is probed with `check_cxx_compiler_flag` before use, because half of them are a
hard error off x86: `-fstack-clash-protection` is x86/s390x only, `-fcf-protection` is x86 only, and
Apple's linker rejects `-z` outright. The answer is cached in `CMakeCache.txt`, so it costs one
configure rather than one build.

Details and the full flag tables are in the README, under
[Warnings and hardening](../README.md#warnings-and-hardening) and
[Finding memory bugs and data races](../README.md#finding-memory-bugs-and-data-races).

## What cppm does not claim

Overclaiming on safety is how a tool like this loses its credibility, so to be precise:

**cppm does not make C++ memory safe.** No borrow checker, no lifetime analysis, no ownership
model. A determined use-after-free will still reach your program.

What it does is move a class of bug from *found by a user* to *found by the first test run*, on a
default project, without reading a flags manual first. If you already run ASan in CI at the right
`-O` level with hardened release builds, cppm has little to teach you — and the reason most projects
do not is almost never that someone weighed it and decided against.

Also deliberately out of scope:

- **No version solving and no ranges.** A version is an opaque git ref — tag, branch or commit — and
  cppm never parses, orders or compares it. This looks like a missing feature and is a chosen one:
  it means a library that ignores semver is not a problem.
- **No registry.** Dependencies are git URLs, so there is no index to maintain and no gate to pass.
- **No transitive pinning.** cppm pins what cppm fetches; what a dependency fetches is its business.
- **Not a CMake replacement**, and no claim to generate final CMake.

Those boundaries are what make the rest tractable. Target discovery gets to be an ~80%-accurate
convenience with an honest hand-off for the remainder, instead of a correctness guarantee it could
never meet.

## And the tool itself carries no dependencies

cppm is Go with an empty `go.sum`, and CI fails if that changes. A tool whose purpose is to relieve
dependency pain should not carry any of its own.
