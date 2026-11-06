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

	"cppm/internal/atomicfile"
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
type CMake struct {
	Minimum string `json:"minimum"`
}

// Target is a build target cppm scaffolds. v1 generates exactly one.
type Target struct {
	Kind         string   `json:"kind"`
	Sources      []string `json:"sources"`
	Dependencies []string `json:"dependencies,omitempty"`
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
}

// Manifest is the whole of cppm.json. Field order here is the serialised order.
type Manifest struct {
	Schema       int                   `json:"schema"`
	Name         string                `json:"name"`
	Version      string                `json:"version"`
	CXX          CXX                   `json:"cxx"`
	CMake        CMake                 `json:"cmake"`
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
		CMake: CMake{Minimum: DefaultCMakeMinimum},
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
	for name, t := range m.Targets {
		if !nameRe.MatchString(name) {
			return fmt.Errorf("invalid target name %q", name)
		}
		switch t.Kind {
		case KindExecutable, KindLibrary:
		default:
			return fmt.Errorf("target %q: invalid kind %q (expected %q or %q)", name, t.Kind, KindExecutable, KindLibrary)
		}
		if len(t.Sources) == 0 {
			return fmt.Errorf("target %q: at least one source is required", name)
		}
		for _, d := range t.Dependencies {
			if _, ok := m.Dependencies[d]; !ok {
				return fmt.Errorf("target %q depends on %q, which is not in dependencies", name, d)
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

// TargetNames returns target names in deterministic order.
func (m *Manifest) TargetNames() []string {
	names := make([]string, 0, len(m.Targets))
	for n := range m.Targets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func validStandard(s int) bool {
	for _, v := range ValidStandards {
		if v == s {
			return true
		}
	}
	return false
}
