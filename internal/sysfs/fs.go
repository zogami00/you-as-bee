// Package sysfs enumerates USB devices from the Linux sysfs tree.
//
// All file access goes through the small FS interface so that enumeration can
// be exercised on Windows (and anywhere else) against an in-memory fake: sysfs
// itself, and symlinks inside it, are Linux-only.
package sysfs

import (
	"os"
	"path/filepath"
)

// FS abstracts the filesystem operations used to describe sysfs. The real
// implementation is OSFS; tests use the in-memory FakeFS in fake.go.
type FS interface {
	// ReadFile returns the contents of the named file.
	ReadFile(name string) ([]byte, error)
	// ReadDir lists the entries of the named directory.
	ReadDir(name string) ([]DirEntry, error)
	// ReadLink returns the target of the named symbolic link.
	ReadLink(name string) (string, error)
	// Stat reports whether name exists and whether it is a directory.
	Stat(name string) (FileInfo, error)
}

// DirEntry is one entry in a sysfs directory.
type DirEntry struct {
	Name string
	Dir  bool
}

// FileInfo is the subset of file metadata Enumerate needs.
type FileInfo struct {
	Dir    bool
	Exists bool
}

// OSFS is the production FS backed by the operating system.
type OSFS struct{}

// ReadFile implements FS.
func (OSFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

// ReadDir implements FS.
//
// Every entry under /sys/bus/usb/devices is a symlink to the real device
// directory, and os.DirEntry.IsDir reports false for a symlink. An entry is
// therefore only a directory when it is one itself or when it is a symlink
// that resolves to a directory. A broken symlink degrades to a non-directory
// rather than failing the whole listing.
func (OSFS) ReadDir(name string) ([]DirEntry, error) {
	ents, err := os.ReadDir(name)
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(ents))
	for _, e := range ents {
		dir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			fi, statErr := os.Stat(filepath.Join(name, e.Name()))
			dir = statErr == nil && fi.IsDir()
		}
		out = append(out, DirEntry{Name: e.Name(), Dir: dir})
	}
	return out, nil
}

// ReadLink implements FS.
func (OSFS) ReadLink(name string) (string, error) { return os.Readlink(name) }

// Stat implements FS.
func (OSFS) Stat(name string) (FileInfo, error) {
	fi, err := os.Stat(name)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Dir: fi.IsDir(), Exists: true}, nil
}
