package sysfs

import (
	"fmt"
	"path"
	"strings"
	"sync"
)

// FakeFS is an in-memory FS used by tests. It supports regular files, symbolic
// links and directories, which is everything Enumerate needs. It is safe for
// concurrent use.
type FakeFS struct {
	mu    sync.RWMutex
	files map[string]string
	links map[string]string
	dirs  map[string]bool
}

// NewFakeFS returns an empty FakeFS.
func NewFakeFS() *FakeFS {
	return &FakeFS{
		files: make(map[string]string),
		links: make(map[string]string),
		dirs:  make(map[string]bool),
	}
}

// AddFile stores a regular file with the given contents. Parent directories are
// implied and do not need to be added first.
func (f *FakeFS) AddFile(name, content string) *FakeFS {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[clean(name)] = content
	return f
}

// AddLink stores a symbolic link pointing at target.
func (f *FakeFS) AddLink(name, target string) *FakeFS {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.links[clean(name)] = target
	return f
}

// AddDir records an otherwise empty directory.
func (f *FakeFS) AddDir(name string) *FakeFS {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[clean(name)] = true
	return f
}

// ReadFile implements FS.
func (f *FakeFS) ReadFile(name string) ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	name = clean(name)
	if content, ok := f.files[name]; ok {
		return []byte(content), nil
	}
	return nil, fmt.Errorf("sysfs: read %s: no such file", name)
}

// ReadDir implements FS.
func (f *FakeFS) ReadDir(name string) ([]DirEntry, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	name = clean(name)
	if !f.dirExistsLocked(name) {
		return nil, fmt.Errorf("sysfs: readdir %s: no such directory", name)
	}
	seen := make(map[string]bool)
	var out []DirEntry
	add := func(child string) {
		if child == "" || seen[child] {
			return
		}
		seen[child] = true
		out = append(out, DirEntry{Name: child, Dir: f.dirExistsLocked(path.Join(name, child))})
	}
	prefix := name + "/"
	if name == "/" {
		prefix = "/"
	}
	for k := range f.files {
		if strings.HasPrefix(k, prefix) {
			add(firstSegment(k[len(prefix):]))
		}
	}
	for k := range f.links {
		if strings.HasPrefix(k, prefix) {
			add(firstSegment(k[len(prefix):]))
		}
	}
	for k := range f.dirs {
		if strings.HasPrefix(k, prefix) {
			add(firstSegment(k[len(prefix):]))
		}
	}
	return out, nil
}

func firstSegment(rest string) string {
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return rest
}

// ReadLink implements FS.
func (f *FakeFS) ReadLink(name string) (string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	name = clean(name)
	if target, ok := f.links[name]; ok {
		return target, nil
	}
	return "", fmt.Errorf("sysfs: readlink %s: not a symlink", name)
}

// Stat implements FS.
func (f *FakeFS) Stat(name string) (FileInfo, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	name = clean(name)
	if f.dirExistsLocked(name) {
		return FileInfo{Dir: true, Exists: true}, nil
	}
	if _, ok := f.files[name]; ok {
		return FileInfo{Exists: true}, nil
	}
	if _, ok := f.links[name]; ok {
		return FileInfo{Exists: true}, nil
	}
	return FileInfo{}, fmt.Errorf("sysfs: stat %s: no such file", name)
}

// dirExistsLocked reports whether name is a directory (explicit or implied by a
// child entry). The caller must hold at least the read lock.
func (f *FakeFS) dirExistsLocked(name string) bool {
	if f.dirs[name] {
		return true
	}
	prefix := name + "/"
	for k := range f.files {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	for k := range f.links {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	for k := range f.dirs {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

func clean(name string) string {
	if name == "" {
		return "/"
	}
	return path.Clean(name)
}
