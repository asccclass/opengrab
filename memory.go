package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	memoryRoot       = "memory"
	memoryRawDir     = "memory/raw"
	memoryWikiDir    = "memory/wiki"
	memoryPagesDir   = "memory/wiki/pages"
	memorySchemaPath = "MEMORY.md"
	memoryIndexPath  = "memory/wiki/index.md"
	memoryLogPath    = "memory/wiki/log.md"
)

type MemoryStore struct {
	Root       string
	RawDir     string
	WikiDir    string
	PagesDir   string
	SchemaPath string
	IndexPath  string
	LogPath    string
}

func newMemoryStore() MemoryStore {
	return MemoryStore{
		Root:       memoryRoot,
		RawDir:     memoryRawDir,
		WikiDir:    memoryWikiDir,
		PagesDir:   memoryPagesDir,
		SchemaPath: memorySchemaPath,
		IndexPath:  memoryIndexPath,
		LogPath:    memoryLogPath,
	}
}

func (m MemoryStore) Init() error {
	for _, dir := range []string{m.Root, m.RawDir, m.WikiDir, m.PagesDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	if err := ensureFile(m.SchemaPath, defaultMemorySchema()); err != nil {
		return err
	}
	if err := ensureFile(m.IndexPath, defaultMemoryIndex()); err != nil {
		return err
	}
	if err := ensureFile(m.LogPath, defaultMemoryLog()); err != nil {
		return err
	}
	return nil
}

func (m MemoryStore) Context() string {
	schema := readTextOrEmpty(m.SchemaPath, 6000)
	index := readTextOrEmpty(m.IndexPath, 8000)
	log := readTextOrEmpty(m.LogPath, 4000)
	if strings.TrimSpace(schema+index+log) == "" {
		return ""
	}

	return strings.TrimSpace(fmt.Sprintf(`# Persistent Memory

Use the following local wiki as durable memory for this assistant. Treat raw sources as immutable, wiki pages as maintained synthesis, and log as chronological history.

## Schema
%s

## Wiki Index
%s

## Recent Log
%s
`, schema, index, tailLog(log, 20)))
}

func (m MemoryStore) Remember(userInput, assistantReply string) error {
	now := time.Now()
	title := firstLine(userInput)
	if title == "" {
		title = "conversation"
	}

	slug := slugify(title)
	if slug == "" {
		slug = "conversation"
	}
	pageName := fmt.Sprintf("%s-%s.md", now.Format("20060102-150405"), slug)
	pagePath := filepath.Join(m.PagesDir, pageName)
	relPage := filepath.ToSlash(filepath.Join("pages", pageName))

	page := fmt.Sprintf(`---
type: conversation
created: %s
tags:
  - memory
---

# %s

## User

%s

## Assistant

%s
`, now.Format(time.RFC3339), markdownTitle(title), userInput, strings.TrimSpace(assistantReply))

	if err := os.WriteFile(pagePath, []byte(page), 0644); err != nil {
		return err
	}

	if err := appendFile(m.LogPath, fmt.Sprintf("\n## [%s] query | %s\n\n- Page: [[%s]]\n- User: %s\n", now.Format("2006-01-02 15:04:05"), title, relPage, oneLine(userInput, 180))); err != nil {
		return err
	}

	return insertIndexEntry(m.IndexPath, fmt.Sprintf("- [[%s]] - %s\n", relPage, oneLine(userInput, 120)))
}

func ensureFile(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func appendFile(path, content string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(content)
	return err
}

func insertIndexEntry(path, entry string) error {
	content := readTextOrEmpty(path, 0)
	if strings.Contains(content, entry) {
		return nil
	}
	marker := "## Conversation Pages\n"
	if !strings.Contains(content, marker) {
		content = strings.TrimRight(content, "\n") + "\n\n" + marker
	}
	content = strings.Replace(content, marker, marker+entry, 1)
	return os.WriteFile(path, []byte(content), 0644)
}

func readTextOrEmpty(path string, limit int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := string(data)
	if limit > 0 && len(text) > limit {
		return text[len(text)-limit:]
	}
	return text
}

func tailLog(text string, maxEntries int) string {
	parts := strings.Split(text, "\n## [")
	if len(parts) <= maxEntries+1 {
		return text
	}
	head := strings.TrimSpace(parts[0])
	tail := parts[len(parts)-maxEntries:]
	for i := range tail {
		if !strings.HasPrefix(tail[i], "## [") {
			tail[i] = "## [" + tail[i]
		}
	}
	return head + "\n\n" + strings.Join(tail, "\n")
}

func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return oneLine(line, 80)
		}
	}
	return ""
}

func oneLine(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	if max > 0 && len(text) > max {
		return strings.TrimSpace(text[:max]) + "..."
	}
	return text
}

func markdownTitle(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "#", ""))
	if text == "" {
		return "Conversation"
	}
	return text
}

func slugify(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	re := regexp.MustCompile(`[^a-z0-9]+`)
	text = re.ReplaceAllString(text, "-")
	return strings.Trim(text, "-")
}

func defaultMemorySchema() string {
	return `# Memory Schema

This project follows the LLM Wiki pattern:

- ` + "`memory/raw/`" + ` stores immutable source material.
- ` + "`memory/wiki/`" + ` stores LLM-maintained markdown synthesis.
- ` + "`memory/wiki/index.md`" + ` catalogs wiki pages and should be read first.
- ` + "`memory/wiki/log.md`" + ` is append-only chronological history.

When answering, use the wiki context as durable memory. When new durable information appears in conversation, save it as a page, update the index, and append to the log.
`
}

func defaultMemoryIndex() string {
	return `# Memory Index

## Conversation Pages
`
}

func defaultMemoryLog() string {
	return `# Memory Log
`
}
