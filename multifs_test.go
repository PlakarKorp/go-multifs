package multifs

import (
	"errors"
	"io/fs"
	"sort"
	"testing"
	"testing/fstest"
)

func TestMountAndOpen(t *testing.T) {
	mux := NewMultiFS()

	fs1 := fstest.MapFS{
		"foo.txt":             &fstest.MapFile{Data: []byte("hello from fs1")},
		"dir1/bar.txt":        &fstest.MapFile{Data: []byte("bar in fs1")},
		"dir1/subdir/baz.txt": &fstest.MapFile{Data: []byte("baz in fs1")},
	}
	fs2 := fstest.MapFS{
		"qux.txt": &fstest.MapFile{Data: []byte("hello from fs2")},
	}

	if err := mux.Mount("one", fs1); err != nil {
		t.Fatalf("Mount one: %v", err)
	}
	if err := mux.Mount("two", fs2); err != nil {
		t.Fatalf("Mount two: %v", err)
	}

	// Verify reading from fs1 via "one/..."
	data, err := fs.ReadFile(mux, "one/foo.txt")
	if err != nil {
		t.Fatalf("ReadFile one/foo.txt: %v", err)
	}
	if got := string(data); got != "hello from fs1" {
		t.Fatalf("unexpected data: %q", got)
	}

	// Verify reading from fs2 via "two/..."
	data, err = fs.ReadFile(mux, "two/qux.txt")
	if err != nil {
		t.Fatalf("ReadFile two/qux.txt: %v", err)
	}
	if got := string(data); got != "hello from fs2" {
		t.Fatalf("unexpected data: %q", got)
	}
}

func TestRootReadDirListsMountedIDs(t *testing.T) {
	mux := NewMultiFS()

	fs1 := fstest.MapFS{"a.txt": &fstest.MapFile{Data: []byte("a")}}
	fs2 := fstest.MapFS{"b.txt": &fstest.MapFile{Data: []byte("b")}}

	if err := mux.Mount("snap1", fs1); err != nil {
		t.Fatalf("Mount snap1: %v", err)
	}
	if err := mux.Mount("snap2", fs2); err != nil {
		t.Fatalf("Mount snap2: %v", err)
	}

	entries, err := fs.ReadDir(mux, ".")
	if err != nil {
		t.Fatalf("ReadDir root: %v", err)
	}

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	want := []string{"snap1", "snap2"}
	if len(names) != len(want) {
		t.Fatalf("unexpected number of root entries: got %d, want %d", len(names), len(want))
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("root entry[%d]: got %q, want %q", i, names[i], w)
		}
	}
}

func TestUnmount(t *testing.T) {
	mux := NewMultiFS()

	fs1 := fstest.MapFS{"file.txt": &fstest.MapFile{Data: []byte("x")}}
	if err := mux.Mount("snap", fs1); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// Can open before unmount
	if _, err := fs.ReadFile(mux, "snap/file.txt"); err != nil {
		t.Fatalf("ReadFile before unmount: %v", err)
	}

	// Unmount
	if err := mux.Unmount("snap"); err != nil {
		t.Fatalf("Unmount: %v", err)
	}

	// Now should fail
	if _, err := fs.ReadFile(mux, "snap/file.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected ErrNotExist after unmount, got %v", err)
	}

	// Root should not list "snap" anymore
	entries, err := fs.ReadDir(mux, ".")
	if err != nil {
		t.Fatalf("ReadDir root after unmount: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "snap" {
			t.Fatalf("unmounted id still present in root listing")
		}
	}
}

func TestStatAndReadDirOnSubdir(t *testing.T) {
	mux := NewMultiFS()

	fs1 := fstest.MapFS{
		"dir/file.txt": &fstest.MapFile{Data: []byte("hello")},
	}
	if err := mux.Mount("one", fs1); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// Stat a file
	info, err := mux.Stat("one/dir/file.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.IsDir() {
		t.Fatalf("Stat returned a directory for a file")
	}
	if info.Name() != "file.txt" {
		t.Fatalf("Stat.Name: got %q, want %q", info.Name(), "file.txt")
	}

	// ReadDir on subdir
	entries, err := mux.ReadDir("one/dir")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ReadDir length: got %d, want 1", len(entries))
	}
	if entries[0].Name() != "file.txt" {
		t.Fatalf("ReadDir entry Name: got %q, want %q", entries[0].Name(), "file.txt")
	}
}

func TestInvalidMountIDs(t *testing.T) {
	mux := NewMultiFS()
	fs1 := fstest.MapFS{}

	if err := mux.Mount("", fs1); err == nil {
		t.Fatalf("expected error for empty id, got nil")
	}

	if err := mux.Mount("with/slash", fs1); err == nil {
		t.Fatalf("expected error for id with slash, got nil")
	}

	if err := mux.Mount("ok", nil); err == nil {
		t.Fatalf("expected error for nil fs, got nil")
	}
}

func TestInvalidPaths(t *testing.T) {
	mux := NewMultiFS()
	fs1 := fstest.MapFS{"file.txt": &fstest.MapFile{Data: []byte("x")}}
	if err := mux.Mount("one", fs1); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	tests := []string{
		"/absolute",
		"../outside",
		"one/../escape",
	}

	for _, name := range tests {
		_, err := mux.Open(name)
		if err == nil {
			t.Fatalf("Open(%q): expected error, got nil", name)
		}
	}
}

func TestNonExistentIDOrFile(t *testing.T) {
	mux := NewMultiFS()
	fs1 := fstest.MapFS{"file.txt": &fstest.MapFile{Data: []byte("x")}}
	if err := mux.Mount("one", fs1); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// Non-existent id
	if _, err := fs.ReadFile(mux, "two/file.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected ErrNotExist for unknown id, got %v", err)
	}

	// Non-existent file in existing id
	if _, err := fs.ReadFile(mux, "one/missing.txt"); err == nil {
		t.Fatalf("expected error for missing file, got nil")
	}
}

func TestRootOpenAsDir(t *testing.T) {
	mux := NewMultiFS()
	fs1 := fstest.MapFS{"file.txt": &fstest.MapFile{Data: []byte("x")}}
	if err := mux.Mount("one", fs1); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// Open(".") should be a directory with ReadDirFile
	f, err := mux.Open(".")
	if err != nil {
		t.Fatalf("Open(.): %v", err)
	}
	defer f.Close()

	if _, ok := f.(fs.ReadDirFile); !ok {
		t.Fatalf("root did not implement fs.ReadDirFile")
	}
}
