package multifs

import (
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
	"time"
)

type MultiFS struct {
	mu    sync.RWMutex
	roots map[string]fs.FS
}

func NewMultiFS() *MultiFS {
	return &MultiFS{
		roots: make(map[string]fs.FS),
	}
}

func (m *MultiFS) Mount(id string, f fs.FS) error {
	id = strings.Trim(id, "/")
	if id == "" || strings.Contains(id, "/") {
		return errors.New("multifs: ids must be non-empty single path components")
	}
	if f == nil {
		return errors.New("multifs: fs is nil")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.roots[id] = f
	return nil
}

func (m *MultiFS) Unmount(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.roots[id]; !ok {
		return fs.ErrNotExist
	}
	delete(m.roots, id)
	return nil
}

func (m *MultiFS) getRoot(id string) (fs.FS, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.roots[id]
	return f, ok
}

func (m *MultiFS) idsSnapshot() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.roots))
	for k := range m.roots {
		names = append(names, k)
	}
	return names
}

func (m *MultiFS) split(name string) (id, subpath string, err error) {
	// Normalize
	name = path.Clean(name)

	// Make it tolerant of leading slash (e.g. Open("/"), Open("/one/file"))
	if strings.HasPrefix(name, "/") {
		name = strings.TrimPrefix(name, "/")
	}

	// Also tolerate "./"
	name = strings.TrimPrefix(name, "./")

	// Root?
	if name == "" || name == "." {
		return "", ".", nil
	}

	// still forbid attempts to escape
	if name == ".." || strings.HasPrefix(name, "../") {
		return "", "", fs.ErrNotExist
	}

	parts := strings.SplitN(name, "/", 2)
	id = parts[0]

	_, ok := m.getRoot(id)
	if !ok {
		return "", "", fs.ErrNotExist
	}

	if len(parts) == 1 {
		subpath = "."
	} else {
		subpath = parts[1]
	}
	return id, subpath, nil
}

func (m *MultiFS) Open(name string) (fs.File, error) {
	id, subpath, err := m.split(name)
	if err != nil {
		return nil, err
	}

	if id == "" {
		// synthetic global root listing all ids
		return newRootDir(m.idsSnapshot()), nil
	}

	subfs, ok := m.getRoot(id)
	if !ok {
		return nil, fs.ErrNotExist
	}

	// Root of that snapshot: "/<id>/" or "id"
	if subpath == "." {
		return newSnapshotRootDir(subfs), nil
	}

	// Normal delegated open inside that snapshot
	return subfs.Open(subpath)
}

type rootDir struct {
	names []string
	pos   int
}

func newRootDir(names []string) *rootDir {
	return &rootDir{names: names}
}

var _ fs.File = (*rootDir)(nil)
var _ fs.ReadDirFile = (*rootDir)(nil)

func (d *rootDir) Stat() (fs.FileInfo, error) { return dirInfo{name: "."}, nil }
func (d *rootDir) Read([]byte) (int, error)   { return 0, io.EOF }
func (d *rootDir) Close() error               { return nil }

func (d *rootDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if d.pos >= len(d.names) && n > 0 {
		return nil, io.EOF
	}
	if n <= 0 || n > len(d.names)-d.pos {
		n = len(d.names) - d.pos
	}

	entries := make([]fs.DirEntry, 0, n)
	for ; n > 0 && d.pos < len(d.names); n-- {
		entries = append(entries, dirEntry{name: d.names[d.pos]})
		d.pos++
	}
	return entries, nil
}

type dirInfo struct {
	name string
}

func (i dirInfo) Name() string       { return i.name }
func (i dirInfo) Size() int64        { return 0 }
func (i dirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o555 }
func (i dirInfo) ModTime() time.Time { return time.Time{} }
func (i dirInfo) IsDir() bool        { return true }
func (i dirInfo) Sys() any           { return nil }

type dirEntry struct {
	name string
}

func (e dirEntry) Name() string               { return e.name }
func (e dirEntry) IsDir() bool                { return true }
func (e dirEntry) Type() fs.FileMode          { return fs.ModeDir }
func (e dirEntry) Info() (fs.FileInfo, error) { return dirInfo{name: e.name}, nil }

var _ fs.StatFS = (*MultiFS)(nil)
var _ fs.ReadDirFS = (*MultiFS)(nil)

func (m *MultiFS) Stat(name string) (fs.FileInfo, error) {
	f, err := m.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

func (m *MultiFS) ReadDir(name string) ([]fs.DirEntry, error) {
	f, err := m.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dir, ok := f.(fs.ReadDirFile)
	if !ok {
		return nil, errors.New("not a directory")
	}
	return dir.ReadDir(-1)
}

type snapshotRootDir struct {
	fs  fs.FS
	pos int
	buf []fs.DirEntry
}

func newSnapshotRootDir(f fs.FS) *snapshotRootDir {
	return &snapshotRootDir{fs: f}
}

// Make sure it implements fs.File and fs.ReadDirFile
var _ fs.File = (*snapshotRootDir)(nil)
var _ fs.ReadDirFile = (*snapshotRootDir)(nil)

func (d *snapshotRootDir) Stat() (fs.FileInfo, error) {
	// Just say "directory"; name doesn't matter much here
	return dirInfo{name: "."}, nil
}

func (d *snapshotRootDir) Read([]byte) (int, error) { return 0, io.EOF }
func (d *snapshotRootDir) Close() error             { return nil }

func (d *snapshotRootDir) ReadDir(n int) ([]fs.DirEntry, error) {
	// Load children once
	if d.buf == nil {
		entries, err := fs.ReadDir(d.fs, ".")
		if err != nil {
			return nil, err
		}
		d.buf = entries
	}

	if d.pos >= len(d.buf) && n > 0 {
		return nil, io.EOF
	}

	if n <= 0 || n > len(d.buf)-d.pos {
		n = len(d.buf) - d.pos
	}

	out := d.buf[d.pos : d.pos+n]
	d.pos += n
	return out, nil
}
