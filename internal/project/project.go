// Package project locates a cppm project on disk and holds its manifest and lock.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mehdi-shokohi/cppm/internal/cmakegen"
	"github.com/mehdi-shokohi/cppm/internal/lockfile"
	"github.com/mehdi-shokohi/cppm/internal/manifest"
)

// DirName is the cppm-owned directory. Everything inside it is generated or
// downloaded and is excluded from git.
const DirName = ".cppm"

// ErrNotFound is returned when no cppm.json exists in the cwd or any parent.
var ErrNotFound = errors.New("not a cppm project (no cppm.json found in this directory or any parent)")

// Project is an opened cppm project.
type Project struct {
	Root     string
	Manifest *manifest.Manifest
	Lock     *lockfile.Lock
}

// Find walks up from start looking for cppm.json and returns the directory
// containing it.
func Find(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, manifest.FileName)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNotFound
		}
		dir = parent
	}
}

// Open finds the project root from start and loads its manifest and lock.
//
// A missing lockfile is not an error; it is treated as "nothing locked yet",
// which is the state right after `cppm init`.
func Open(start string) (*Project, error) {
	root, err := Find(start)
	if err != nil {
		return nil, err
	}
	m, err := manifest.Load(filepath.Join(root, manifest.FileName))
	if err != nil {
		return nil, err
	}
	p := &Project{Root: root, Manifest: m}

	l, err := lockfile.Load(p.LockPath())
	switch {
	case err == nil:
		p.Lock = l
	case os.IsNotExist(err):
		p.Lock = lockfile.New("unknown")
	default:
		return nil, err
	}
	return p, nil
}

// ManifestPath is <root>/cppm.json.
func (p *Project) ManifestPath() string {
	return filepath.Join(p.Root, manifest.FileName)
}

// LockPath is <root>/cppm.lock.
func (p *Project) LockPath() string {
	return filepath.Join(p.Root, lockfile.FileName)
}

// GeneratedDir is <root>/.cppm/generated.
func (p *Project) GeneratedDir() string {
	return filepath.Join(p.Root, DirName, "generated")
}

// ManagedCMakePath is the single cppm-owned CMake include: language settings
// plus dependencies.
func (p *Project) ManagedCMakePath() string {
	return filepath.Join(p.Root, filepath.FromSlash(cmakegen.GeneratedRelPath))
}

// CMakeListsPath is the user-owned CMakeLists.txt. cppm writes it at init and
// never again.
func (p *Project) CMakeListsPath() string {
	return filepath.Join(p.Root, "CMakeLists.txt")
}

// PresetsPath is <root>/CMakePresets.json.
func (p *Project) PresetsPath() string {
	return filepath.Join(p.Root, "CMakePresets.json")
}

// BuildDir is the build tree for a named preset.
func (p *Project) BuildDir(preset string) string {
	return filepath.Join(p.Root, "build", preset)
}

// SaveManifest writes the manifest atomically.
func (p *Project) SaveManifest() error {
	if err := p.Manifest.Save(p.ManifestPath()); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}
	return nil
}

// SaveLock writes the lockfile atomically.
func (p *Project) SaveLock() error {
	if err := p.Lock.Save(p.LockPath()); err != nil {
		return fmt.Errorf("save lockfile: %w", err)
	}
	return nil
}
