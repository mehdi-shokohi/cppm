// Package manifest reads and writes cppm.json, the project's statement of intent.
//
// The manifest says what the project wants; the lockfile says what will actually
// be fetched. See docs/architecture.md section 2.
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/mehdi-shokohi/cppm/internal/atomicfile"
	"github.com/mehdi-shokohi/cppm/internal/sources"
)

// FileName is the manifest's fixed name, found by walking up from the cwd.
const FileName = "cppm.json"

// Schema is the current manifest schema version. Bump only on a breaking change.
const Schema = 1

// Dependency source kinds. This is an open set: "archive" and "find_package"
// are plausible later additions, which is why the schema stores a source string
// rather than encoding GitHub specially.
const (
	SourceGit  = "git"
	SourcePath = "path"
)

// Target kinds.
const (
	KindExecutable = "executable"
	KindLibrary    = "library"
)

// CXX is the language standard configuration.
//
// Extensions is deliberately a plain bool defaulting to false: CMake's
// CMAKE_CXX_EXTENSIONS defaults to ON, which silently compiles the user's own
// code as gnu++NN and produces mixed dialects across dependencies. See
// docs/architecture.md section 3c.
type CXX struct {
	Standard   int  `json:"standard"`
	Required   bool `json:"required"`
	Extensions bool `json:"extensions"`
}

// CMake holds CMake-level settings for the generated project.
//
// Every field is written out explicitly by `cppm init`, even at its default, so
// that the file shows what can be changed. A setting nobody can see is a
// setting nobody can use.
type CMake struct {
	Minimum string `json:"minimum"`

	// ExportCompileCommands produces compile_commands.json, which clangd,
	// ccls and most editors need for completion and diagnostics.
	//
	// A pointer so that an absent field means "default" rather than "false".
	ExportCompileCommands *bool `json:"exportCompileCommands,omitempty"`

	// BuildTypes maps cppm's preset names to CMake build types. Release
	// defaults to RelWithDebInfo because a release build with no symbols is
	// very hard to debug in the field; set it to "Release" for a smaller build.
	BuildTypes map[string]string `json:"buildTypes,omitempty"`
}

// DefaultBuildTypes are the preset-to-CMake-build-type mappings.
func DefaultBuildTypes() map[string]string {
	return map[string]string{"debug": "Debug", "release": "RelWithDebInfo"}
}

// WantCompileCommands reports whether to export compile_commands.json.
func (c CMake) WantCompileCommands() bool {
	if c.ExportCompileCommands == nil {
		return true
	}
	return *c.ExportCompileCommands
}

// BuildTypeFor returns the CMake build type for a preset name.
func (c CMake) BuildTypeFor(preset string) string {
	if t, ok := c.BuildTypes[preset]; ok && t != "" {
		return t
	}
	return DefaultBuildTypes()[preset]
}

// Toolchain selects the compiler and generator.
//
// These cannot be set from the generated include: CMAKE_CXX_COMPILER must be
// known before project() runs, and an include placed after project() is too
// late. They are therefore written into CMakePresets.json as cache variables,
// which is the supported way to choose a compiler per build tree.
//
// All fields are optional; empty means "let CMake decide".
type Toolchain struct {
	// CXX is the C++ compiler: a command found on PATH ("clang++", "g++") or an
	// absolute path.
	CXX string `json:"cxx,omitempty"`
	// C is the C compiler. Left empty by default: generated projects declare
	// LANGUAGES CXX only, so setting it would make CMake warn that it is
	// unused. Set it explicitly once a dependency brings C sources, and add C
	// to the project() call in CMakeLists.txt.
	C string `json:"c,omitempty"`
	// Generator is the CMake generator, e.g. "Ninja" or "Unix Makefiles".
	Generator string `json:"generator,omitempty"`
}

// compilerAliases maps what people type to the driver that actually links a C++
// program. Invoking "clang" or "gcc" as the C++ compiler compiles but fails to
// link the C++ standard library, which produces a wall of undefined references.
var compilerAliases = map[string]string{
	"clang": "clang++",
	"gcc":   "g++",
	"cc":    "c++",
}

// NormalizeCXX returns the correct C++ driver for a compiler name, and reports
// whether it had to change anything.
func NormalizeCXX(name string) (string, bool) {
	base := name
	dir := ""
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		dir, base = name[:i+1], name[i+1:]
	}
	if fixed, ok := compilerAliases[strings.ToLower(base)]; ok {
		return dir + fixed, true
	}
	return name, false
}

// CompanionC returns the C compiler matching a C++ driver.
//
// Dependencies routinely contain C sources (zlib, glfw, SDL), and letting one
// vendor's C++ compiler pair with another's C compiler is a needless source of
// trouble. Generated projects enable the C language so this is actually used.
func CompanionC(cxx string) string {
	base, dir := cxx, ""
	if i := strings.LastIndexAny(cxx, `/\`); i >= 0 {
		dir, base = cxx[:i+1], cxx[i+1:]
	}
	switch strings.ToLower(base) {
	case "clang++":
		return dir + "clang"
	case "g++":
		return dir + "gcc"
	case "c++":
		return dir + "cc"
	}
	return ""
}

// Target is a build target cppm scaffolds. v1 generates exactly one.
type Target struct {
	Kind    string   `json:"kind"`
	Sources []string `json:"sources"`

	// SourceGlobs are patterns expanded by `cppm sync` and attached to the
	// target by the generated CMake, which is the only way cppm can add a
	// source file without editing CMakeLists.txt.
	//
	// Sources above stays the authoritative list of what CMakeLists.txt itself
	// names; these are additions cppm owns. Keeping them in separate fields is
	// what lets `cppm sources` report honestly which files came from where.
	SourceGlobs []string `json:"sourceGlobs,omitempty"`

	Dependencies []string `json:"dependencies,omitempty"`

	// Test marks the target as a test executable: `cppm init --with-tests`
	// registers it with add_test(), and it is the only kind of target a dev
	// dependency may be linked into.
	//
	// This is a declaration, not an inference. cppm never guesses that a target
	// called "foo_tests" is a test, because being wrong either leaks a test
	// framework into a release binary or leaves a test unlinked.
	Test bool `json:"test,omitempty"`
}

// Dependency is one entry in the dependencies map.
//
// Version is an opaque git ref - a tag, branch or commit. cppm never parses,
// orders or compares it. See docs/architecture.md section 3b.
type Dependency struct {
	Source  string            `json:"source"`
	URL     string            `json:"url,omitempty"`
	Path    string            `json:"path,omitempty"`
	Version string            `json:"version,omitempty"`
	Targets []string          `json:"targets,omitempty"`
	Options map[string]string `json:"options,omitempty"`

	// Dev marks a dependency needed only to build and run the tests: Catch2,
	// googletest, doctest, benchmark.
	//
	// It is still fetched and made available, so CPPM_TARGETS_<dep> works, but
	// it is left out of CPPM_DEPENDENCIES. That is what stops
	// cppm_link_dependencies() with no argument list from linking a test
	// framework into a release binary.
	Dev bool `json:"dev,omitempty"`
}

// Manifest is the whole of cppm.json. Field order here is the serialised order.
type Manifest struct {
	Schema       int                   `json:"schema"`
	Name         string                `json:"name"`
	Version      string                `json:"version"`
	CXX          CXX                   `json:"cxx"`
	CMake        CMake                 `json:"cmake"`
	Toolchain    Toolchain             `json:"toolchain,omitempty"`
	Targets      map[string]Target     `json:"targets,omitempty"`
	Dependencies map[string]Dependency `json:"dependencies"`
	Overrides    map[string]string     `json:"overrides,omitempty"`
}

// DefaultStandard is used when the user does not pass --std.
const DefaultStandard = 20

// DefaultCMakeMinimum is the floor written into generated projects.
//
// 3.24 rather than 3.28: the keywords that need a newer CMake (SYSTEM at 3.25,
// EXCLUDE_FROM_ALL at 3.28) are emitted conditionally by the generator instead.
const DefaultCMakeMinimum = "3.24"

// ValidStandards are the C++ standards CMake understands as CMAKE_CXX_STANDARD.
var ValidStandards = []int{11, 14, 17, 20, 23, 26}

// nameRe constrains a project name to something usable as a CMake target.
var nameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)

// New returns a manifest for a new project with one executable target.
func New(name string, std int) *Manifest {
	return &Manifest{
		Schema:  Schema,
		Name:    name,
		Version: "0.1.0",
		CXX: CXX{
			Standard:   std,
			Required:   true,
			Extensions: false,
		},
		CMake: CMake{
			Minimum:               DefaultCMakeMinimum,
			ExportCompileCommands: boolPtr(true),
			BuildTypes:            DefaultBuildTypes(),
		},
		Targets: map[string]Target{
			name: {
				Kind:    KindExecutable,
				Sources: []string{"src/main.cpp"},
			},
		},
		Dependencies: map[string]Dependency{},
	}
}

// Load reads and validates a manifest.
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if m.Dependencies == nil {
		m.Dependencies = map[string]Dependency{}
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &m, nil
}

// Save validates and atomically writes the manifest.
func (m *Manifest) Save(path string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	b, err := m.Bytes()
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o644)
}

// Bytes renders the manifest as the exact JSON that is written to disk.
//
// encoding/json sorts map keys, so output is deterministic and diffs stay clean.
func (m *Manifest) Bytes() ([]byte, error) {
	if m.Dependencies == nil {
		m.Dependencies = map[string]Dependency{}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Validate reports the first structural problem with the manifest.
func (m *Manifest) Validate() error {
	if m.Schema != Schema {
		return fmt.Errorf("unsupported schema version %d (this cppm understands %d)", m.Schema, Schema)
	}
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("invalid project name %q: must start with a letter and contain only letters, digits, '_', '.' or '-'", m.Name)
	}
	if !validStandard(m.CXX.Standard) {
		return fmt.Errorf("invalid cxx.standard %d: expected one of %v", m.CXX.Standard, ValidStandards)
	}
	if m.CMake.Minimum == "" {
		return errors.New("cmake.minimum must not be empty")
	}
	validTypes := map[string]bool{
		"Debug": true, "Release": true, "RelWithDebInfo": true, "MinSizeRel": true, "": true,
	}
	for preset, bt := range m.CMake.BuildTypes {
		if preset != "debug" && preset != "release" {
			return fmt.Errorf("cmake.buildTypes: unknown preset %q (expected \"debug\" or \"release\")", preset)
		}
		if !validTypes[bt] {
			return fmt.Errorf("cmake.buildTypes.%s: %q is not a CMake build type (expected Debug, Release, RelWithDebInfo or MinSizeRel)", preset, bt)
		}
	}
	for field, v := range map[string]string{
		"toolchain.cxx":       m.Toolchain.CXX,
		"toolchain.c":         m.Toolchain.C,
		"toolchain.generator": m.Toolchain.Generator,
	} {
		if v == "" {
			continue
		}
		// A value beginning with '-' would be read as a flag by the tools these
		// are passed to.
		if strings.HasPrefix(v, "-") {
			return fmt.Errorf("%s %q must not begin with '-'", field, v)
		}
		if strings.ContainsAny(v, "\n\r\"") {
			return fmt.Errorf("%s %q contains an invalid character", field, v)
		}
	}
	for name, t := range m.Targets {
		if !nameRe.MatchString(name) {
			return fmt.Errorf("invalid target name %q", name)
		}
		switch t.Kind {
		case KindExecutable, KindLibrary:
		default:
			return fmt.Errorf("target %q: invalid kind %q (expected %q or %q)", name, t.Kind, KindExecutable, KindLibrary)
		}
		if len(t.Sources) == 0 && len(t.SourceGlobs) == 0 {
			return fmt.Errorf("target %q: at least one entry in \"sources\" or \"sourceGlobs\" is required", name)
		}
		for _, g := range t.SourceGlobs {
			if err := sources.Validate(g); err != nil {
				return fmt.Errorf("target %q: %w", name, err)
			}
		}
		for _, d := range t.Dependencies {
			dep, ok := m.Dependencies[d]
			if !ok {
				return fmt.Errorf("target %q depends on %q, which is not in dependencies", name, d)
			}
			// Catching this here is the whole point of the dev flag: a test
			// framework listed on a shipping target is a release binary with
			// Catch2 compiled into it.
			if dep.Dev && !t.Test {
				return fmt.Errorf("target %q depends on %q, which is marked \"dev\": true in cppm.json.\n"+
					"a dev dependency may only be linked into a test target. Either add \"test\": true to\n"+
					"targets.%s, or remove \"dev\": true from dependencies.%s to link it everywhere",
					name, d, name, d)
			}
		}
	}
	for name, d := range m.Dependencies {
		if name == "" {
			return errors.New("dependency name must not be empty")
		}
		switch d.Source {
		case SourceGit:
			if d.URL == "" {
				return fmt.Errorf("dependency %q: source %q requires a url", name, SourceGit)
			}
			if d.Path != "" {
				return fmt.Errorf("dependency %q: source %q must not set path", name, SourceGit)
			}
		case SourcePath:
			if d.Path == "" {
				return fmt.Errorf("dependency %q: source %q requires a path", name, SourcePath)
			}
			if d.URL != "" {
				return fmt.Errorf("dependency %q: source %q must not set url", name, SourcePath)
			}
		default:
			return fmt.Errorf("dependency %q: unknown source %q (expected %q or %q)", name, d.Source, SourceGit, SourcePath)
		}
	}
	return nil
}

// DependencyNames returns dependency names in deterministic order.
func (m *Manifest) DependencyNames() []string {
	names := make([]string, 0, len(m.Dependencies))
	for n := range m.Dependencies {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// DevDependencyNames returns the dev-only dependency names, in order.
func (m *Manifest) DevDependencyNames() []string {
	var names []string
	for _, n := range m.DependencyNames() {
		if m.Dependencies[n].Dev {
			names = append(names, n)
		}
	}
	return names
}

// TestTargetNames returns the names of targets marked "test": true, in order.
func (m *Manifest) TestTargetNames() []string {
	var names []string
	for _, n := range m.TargetNames() {
		if m.Targets[n].Test {
			names = append(names, n)
		}
	}
	return names
}

// TargetNames returns target names in deterministic order.
func (m *Manifest) TargetNames() []string {
	names := make([]string, 0, len(m.Targets))
	for n := range m.Targets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// boolPtr is a helper for optional boolean fields.
func boolPtr(v bool) *bool { return &v }

func validStandard(s int) bool {
	for _, v := range ValidStandards {
		if v == s {
			return true
		}
	}
	return false
}
