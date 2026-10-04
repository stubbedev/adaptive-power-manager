package history

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileIsEmpty(t *testing.T) {
	t.Parallel()
	entries, err := Load(filepath.Join(t.TempDir(), "unplugs"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Load() = %v, want empty", entries)
	}
}

func TestAddAppendsAndTrims(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state", "unplugs")

	if err := Add(path, Entry{Dow: 1, Minute: 600}, 3); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := Add(path, Entry{Dow: 2, Minute: 700}, 3); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := Add(path, Entry{Dow: 3, Minute: 800}, 3); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// A fourth entry pushes the oldest out; the cap keeps the newest three.
	if err := Add(path, Entry{Dow: 4, Minute: 900}, 3); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	entries, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []Entry{{Dow: 2, Minute: 700}, {Dow: 3, Minute: 800}, {Dow: 4, Minute: 900}}
	if len(entries) != len(want) {
		t.Fatalf("Load() = %v, want %v", entries, want)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entries[%d] = %v, want %v", i, entries[i], want[i])
		}
	}
}

func TestLoadSkipsMalformedLines(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "unplugs")
	content := "1 600\nnot a record\n9 700\n2 -5\n3 2500\n4 1030\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	entries, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []Entry{{Dow: 1, Minute: 600}, {Dow: 4, Minute: 1030}}
	if len(entries) != 2 || entries[0] != want[0] || entries[1] != want[1] {
		t.Errorf("Load() = %v, want %v", entries, want)
	}
}
