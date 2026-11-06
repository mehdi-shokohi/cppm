// Package lockfile reads and writes cppm.lock, the record of what will actually
// be fetched.
//
// The manifest states intent; the lock states resolution. A tag is never
// sufficient for reproducibility because tags move, so every git package records
// a full commit hash. See docs/architecture.md section 3.
package lockfile

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"cppm/internal/atomicfile"
)

// FileName is the lockfile's fixed name, a sibling of cppm.json.
const FileName = "cppm.lock"

// Schema is the current lockfile schema version.
const Schema = 1

// Target origin values, recorded so that `cppm info` can explain *why* a target
// name was chosen and `doctor` can flag the ones that were guessed.
const (
	OriginExplicit   = "explicit"   // pinned in cppm.json
	OriginRecipe     = "recipe"     // from the curated database
	OriginInferred   = "inferred"   // static source scan
	OriginProbed     = "probed"     // configure-time enumeration
	OriginUser       = "user"       // chosen at an interactive prompt
	OriginUnresolved = "unresolved" // nothing confident; user must decide
)

// Package is one resolved dependency.
type Package struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	URL       string `json:"url,omitempty"`
	Path      string `json:"path,omitempty"`
	Requested string `json:"requested,omitempty"`

	// ResolvedRef is the ref that Requested matched, e.g. "refs/tags/11.1.4".
	ResolvedRef string `json:"resolved_ref,omitempty"`
	// Commit is the full 40-character hash. Empty only for path dependencies,
	// which are editable by design and therefore never pinned.
	Commit string `json:"commit,omitempty"`

	Targets      []string          `json:"targets,omitempty"`
	TargetOrigin string            `json:"target_origin,omitempty"`
	Options      map[string]string `json:"options,omitempty"`
	Dependencies []string          `json:"dependencies,omitempty"`

	// ShallowOK records whether a shallow fetch of a pinned commit succeeded for
	// this host, so a server that rejects it is not probed again on every sync.
	ShallowOK *bool `json:"shallow_ok,omitempty"`
}

// Lock is the whole of cppm.lock.
//
// Packages is an array sorted by name rather than an object, so serialisation is
// deterministic and diffs stay readable.
type Lock struct {
	Schema      int       `json:"schema"`
	GeneratedBy string    `json:"generated_by"`
	Packages    []Package `json:"packages"`
}

// New returns an empty lock stamped with the producing version.
func New(version string) *Lock {
	return &Lock{
		Schema:      Schema,
		GeneratedBy: "cppm " + version,
		Packages:    []Package{},
	}
}

// Load reads a lockfile. A missing lockfile is not an error: it is reported as
// such by os.IsNotExist, and callers treat it as "nothing locked yet".
func Load(path string) (*Lock, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Lock
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if l.Schema != Schema {
		return nil, fmt.Errorf("%s: unsupported schema version %d (this cppm understands %d)", path, l.Schema, Schema)
	}
	if l.Packages == nil {
		l.Packages = []Package{}
	}
	return &l, nil
}

// Save sorts and atomically writes the lockfile.
func (l *Lock) Save(path string) error {
	b, err := l.Bytes()
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o644)
}

// Bytes renders the lock as the exact JSON written to disk, sorted by name.
func (l *Lock) Bytes() ([]byte, error) {
	l.sort()
	if l.Packages == nil {
		l.Packages = []Package{}
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func (l *Lock) sort() {
	sort.SliceStable(l.Packages, func(i, j int) bool {
		return l.Packages[i].Name < l.Packages[j].Name
	})
}

// Find returns the locked package with the given name.
func (l *Lock) Find(name string) (*Package, bool) {
	for i := range l.Packages {
		if l.Packages[i].Name == name {
			return &l.Packages[i], true
		}
	}
	return nil, false
}

// Put inserts or replaces a package by name.
func (l *Lock) Put(p Package) {
	for i := range l.Packages {
		if l.Packages[i].Name == p.Name {
			l.Packages[i] = p
			l.sort()
			return
		}
	}
	l.Packages = append(l.Packages, p)
	l.sort()
}

// Remove deletes a package by name and reports whether it was present.
func (l *Lock) Remove(name string) bool {
	for i := range l.Packages {
		if l.Packages[i].Name == name {
			l.Packages = append(l.Packages[:i], l.Packages[i+1:]...)
			return true
		}
	}
	return false
}

// Names returns locked package names in sorted order.
func (l *Lock) Names() []string {
	l.sort()
	out := make([]string, 0, len(l.Packages))
	for _, p := range l.Packages {
		out = append(out, p.Name)
	}
	return out
}
