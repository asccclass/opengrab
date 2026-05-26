package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryStoreRememberWritesWikiArtifacts(t *testing.T) {
	t.Chdir(t.TempDir())

	memory := newMemoryStore()
	if err := memory.Init(); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if err := memory.Remember("Remember that the project uses markdown memory.", "Saved."); err != nil {
		t.Fatalf("Remember() error = %v", err)
	}

	pages, err := filepath.Glob(filepath.Join(memory.PagesDir, "*.md"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("expected 1 memory page, got %d", len(pages))
	}

	page, err := os.ReadFile(pages[0])
	if err != nil {
		t.Fatalf("ReadFile(page) error = %v", err)
	}
	if !strings.Contains(string(page), "Remember that the project uses markdown memory.") {
		t.Fatalf("memory page does not contain user input:\n%s", string(page))
	}

	index, err := os.ReadFile(memory.IndexPath)
	if err != nil {
		t.Fatalf("ReadFile(index) error = %v", err)
	}
	if !strings.Contains(string(index), "[[pages/") {
		t.Fatalf("index was not updated:\n%s", string(index))
	}

	log, err := os.ReadFile(memory.LogPath)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	if !strings.Contains(string(log), "query | Remember that the project uses markdown memory.") {
		t.Fatalf("log was not updated:\n%s", string(log))
	}
}
