// Package safety holds the compiler and linker flags behind cppm.json's
// "safety" block: warning levels and release hardening.
//
// It is a flag table, not a policy. It decides nothing about where the flags
// are applied -- that is internal/cmakegen's job, and the distinction matters:
// the same flag is correct per target and wrong at directory scope, where it
// would reach every dependency. See the Safety section of the generated CMake.
//
// Like internal/sources, this package imports nothing of cppm's, so
// internal/manifest can validate against it without an import cycle.
package safety

// Warning levels, as they are spelled in cppm.json.
//
// Four names rather than a flag list, because the point is that the user does
// not have to know the flags. A level is also stable across compilers: "strict"
// means the same intent under GCC, Clang and MSVC even though the flags differ
// completely.
const (
	// WarningsOff emits no warning flags at all, leaving the compiler's
	// defaults. For adopting cppm in a large existing codebase without first
	// drowning in output.
	WarningsOff = "off"
	// WarningsDefault is -Wall -Wextra: what most C++ projects should have had
	// from the start.
	WarningsDefault = "default"
	// WarningsStrict adds the warnings that catch real bugs but need some
	// existing code to be cleaned up first.
	WarningsStrict = "strict"
	// WarningsError is strict plus -Werror. Meant for CI: a new compiler
	// release can introduce a warning your code did not change to earn, and
	// -Werror turns that into a build that will not start.
	WarningsError = "error"
)

// WarningLevels are the accepted values, in increasing strictness. Order is
// used for error messages and for `cppm build --warnings` help text.
var WarningLevels = []string{WarningsOff, WarningsDefault, WarningsStrict, WarningsError}

// ValidWarnings reports whether level is a level cppm knows.
func ValidWarnings(level string) bool {
	for _, l := range WarningLevels {
		if l == level {
			return true
		}
	}
	return false
}

// DefaultWarnings is the level used when cppm.json says nothing.
//
// -Wall -Wextra rather than "off": a tool that generates the build has no
// excuse for generating one with warnings disabled, and these two never fail a
// build on their own. Anything stricter is opt-in, because it does need code
// changes.
const DefaultWarnings = WarningsDefault

// gnuStrict are the strict-level additions for GCC and Clang.
//
// Chosen for signal rather than completeness. Each one here has caught a real
// bug in real code:
//
//	-Wshadow            a local that hides a member, so the member is never set
//	-Wconversion        silent narrowing, the usual source of lost precision
//	-Wold-style-cast    a C cast that quietly became a reinterpret_cast
//	-Wnon-virtual-dtor  delete through a base pointer, leaking the derived part
//	-Wcast-align        a cast that is undefined behaviour on ARM and silent on x86
//	-Woverloaded-virtual a near-miss override that hides the base version
//	-Wdouble-promotion  a float expression silently computed as double
//	-Wformat=2          a printf format that does not match its arguments
//
// Deliberately absent: -Wsign-conversion (fires constantly on correct
// container indexing) and -Weffc++ (advice, not bugs). A level nobody can
// leave on is a level nobody turns on.
var gnuStrict = []string{
	"-Wpedantic",
	"-Wshadow",
	"-Wconversion",
	"-Wold-style-cast",
	"-Wnon-virtual-dtor",
	"-Wcast-align",
	"-Wunused",
	"-Woverloaded-virtual",
	"-Wdouble-promotion",
	"-Wformat=2",
}

// GNUWarnings returns the warning flags for GCC and Clang at a level.
func GNUWarnings(level string) []string {
	switch level {
	case WarningsDefault:
		return []string{"-Wall", "-Wextra"}
	case WarningsStrict:
		return append([]string{"-Wall", "-Wextra"}, gnuStrict...)
	case WarningsError:
		return append(GNUWarnings(WarningsStrict), "-Werror")
	default:
		return nil
	}
}

// MSVCWarnings returns the warning flags for MSVC at a level.
//
// /permissive- is not a warning flag and is here on purpose: without it MSVC
// accepts code that no other compiler does, so warnings turned up on MSVC
// alone still let non-portable code through. It is what makes "strict" mean
// the same thing on Windows as it does elsewhere.
func MSVCWarnings(level string) []string {
	switch level {
	case WarningsDefault:
		return []string{"/W3"}
	case WarningsStrict:
		return []string{"/W4", "/permissive-"}
	case WarningsError:
		return []string{"/W4", "/permissive-", "/WX"}
	default:
		return nil
	}
}

// DefaultHardening is whether hardening is on when cppm.json says nothing.
//
// On. Every flag below is either free or close to it, and the ones that are not
// universally supported are probed rather than assumed.
const DefaultHardening = true

// GNUHardenDefines are hardening defines that are safe in every configuration.
//
// _GLIBCXX_ASSERTIONS bounds-checks std::vector::operator[], std::string and
// the iterator ranges, and it is explicitly ABI-compatible: a translation unit
// built with it links against one built without. That is the whole reason it
// can be switched on here, and the reason _GLIBCXX_DEBUG cannot -- that one
// changes the layout of std:: types and needs every dependency rebuilt with
// it, which is a separate feature.
//
// Harmless under libc++, which ignores it; libc++ has its own
// _LIBCPP_HARDENING_MODE, which lands with the debug standard library work.
var GNUHardenDefines = []string{"-D_GLIBCXX_ASSERTIONS"}

// GNUFortify is the _FORTIFY_SOURCE pair, for optimised configurations only.
//
// Two flags, and both are needed. _FORTIFY_SOURCE replaces memcpy, sprintf and
// friends with length-checking variants, but it is implemented in glibc's
// headers using __builtin_object_size, which only computes anything once the
// optimiser runs: at -O0 it silently does nothing. And the -U has to come
// first because Ubuntu, Fedora and SUSE already define it in their GCC specs,
// where a bare -D would produce a "macro redefined" warning on every single
// translation unit.
//
// Still =2 rather than =3. Level 3 is stronger but needs glibc 2.35 with GCC 12
// or Clang 14, and a mismatch there is a compile error rather than a
// degradation, so it would have to be probed to be safe.
var GNUFortify = []string{"-U_FORTIFY_SOURCE", "-D_FORTIFY_SOURCE=2"}

// OptimisedConfigs are the CMake build types where GNUFortify applies.
//
// Named explicitly rather than written as "not Debug": with a multi-config
// generator, or before CMAKE_BUILD_TYPE is set, the configuration is the empty
// string, and "not Debug" would be true there and apply the flag to an
// unoptimised build -- exactly the case it does nothing in.
var OptimisedConfigs = []string{"Release", "RelWithDebInfo", "MinSizeRel"}

// GNUHardenProbed are hardening compile flags that must be tested before use.
//
// None of these is portable, and the failure is not a warning:
// -fstack-clash-protection is x86 and s390x only, -fcf-protection is x86 only,
// and both GCC and Clang reject them outright on aarch64 -- so hardcoding them
// means cppm produces projects that do not compile on an Apple M-series machine
// or a Raspberry Pi. check_cxx_compiler_flag answers per compiler and target
// and caches the answer, which is the only correct way to use them.
var GNUHardenProbed = []string{
	"-fstack-protector-strong",
	"-fstack-clash-protection",
	"-fcf-protection",
}

// GNUHardenLinkProbed are hardening link flags, probed for the same reason.
//
// -z relro -z now make the ELF relocation tables read-only after startup,
// which closes the GOT-overwrite route to code execution. Both are GNU ld and
// lld only: Apple's linker rejects -z, so this has to be probed rather than
// assumed on macOS.
var GNUHardenLinkProbed = []string{
	"-Wl,-z,relro",
	"-Wl,-z,now",
}

// MSVCHardenProbed are the MSVC hardening compile flags.
//
// /sdl turns on the additional security checks that /GS alone does not cover,
// and /guard:cf emits Control Flow Guard metadata. Probed like the others
// because clang-cl accepts the /cl-style driver but not every one of these
// flags.
var MSVCHardenProbed = []string{"/sdl", "/guard:cf"}

// MSVCHardenLinkProbed is the link half of Control Flow Guard.
//
// /guard:cf at compile time only emits the metadata; without /GUARD:CF at link
// time the image is not marked and Windows does not enforce any of it. Setting
// one and not the other is the common mistake, which is why they are a pair
// here.
var MSVCHardenLinkProbed = []string{"/GUARD:CF"}
