package boltstore_test

import (
	//"os"
	"path/filepath"
	"testing"

	"rec/store"
)

func tempDB(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	return path, func() {}
}

func TestSaveAndLoadState(t *testing.T) {
	path, _ := tempDB(t)

	s, err := store.NewStateStore(path)
	if err != nil {
		t.Fatalf("NewStateStore error: %v", err)
	}
	defer s.Close()

	type App struct {
		Name string
		Ver  int
	}

	orig := App{Name: "demo", Ver: 1}

	err = s.SaveState([]string{"apps"}, "demo", orig)
	if err != nil {
		t.Fatalf("SaveState error: %v", err)
	}

	var loaded App
	err = s.LoadState([]string{"apps"}, "demo", &loaded)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}

	if loaded != orig {
		t.Fatalf("mismatch: got %+v want %+v", loaded, orig)
	}
}

func TestLoadMissingKey(t *testing.T) {
	path, _ := tempDB(t)
	s, _ := store.NewStateStore(path)
	defer s.Close()

	var out map[string]any
	err := s.LoadState([]string{"nosuch"}, "missing", &out)
	if err == nil {
		t.Fatalf("expected error for missing key, got nil")
	}
}

func TestConcurrentWrites(t *testing.T) {
	path, _ := tempDB(t)
	s, _ := store.NewStateStore(path)
	defer s.Close()

	type Counter struct {
		Value int
	}

	// 50 concurrent writes to same bucket
	n := 50
	errCh := make(chan error, n)

	for i := 0; i < n; i++ {
		go func(i int) {
			errCh <- s.SaveState([]string{"counters"}, "val", Counter{Value: i})
		}(i)
	}

	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent write error: %v", err)
		}
	}

	// read final
	var c Counter
	err := s.LoadState([]string{"counters"}, "val", &c)
	if err != nil {
		t.Fatalf("LoadState after concurrent writes error: %v", err)
	}

	// we don't know exact final value, but it must be >=0 and <n
	if c.Value < 0 || c.Value >= n {
		t.Fatalf("loaded counter seems wrong: %+v", c)
	}
}

func TestBucketCreationNested(t *testing.T) {
	path, _ := tempDB(t)
	s, _ := store.NewStateStore(path)
	defer s.Close()

	type Data struct{ A int }
	err := s.SaveState([]string{"root", "child", "inner"}, "x", Data{A: 42})
	if err != nil {
		t.Fatalf("SaveState nested error: %v", err)
	}

	var d Data
	err = s.LoadState([]string{"root", "child", "inner"}, "x", &d)
	if err != nil {
		t.Fatalf("Load nested state error: %v", err)
	}
	if d.A != 42 {
		t.Fatalf("expected 42 got %d", d.A)
	}
}

func TestCloseDoesNotPanic(t *testing.T) {
	path, _ := tempDB(t)

	s, err := store.NewStateStore(path)
	if err != nil {
		t.Fatalf("NewStateStore error: %v", err)
	}

	err = s.Close()
	if err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// calling Close again must NOT panic
	_ = s.Close()
}

