package cmdrunner

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type RotateOptions struct {
	StdoutPath string
	StderrPath string
	MaxBytes   int64
	MaxBackups int
	FilePerm   os.FileMode
}

type RotateWriter struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	filePerm   os.FileMode

	file *os.File
	size int64
}

func NewRotateWriter(path string, maxBytes int64, maxBackups int, perm os.FileMode) (*RotateWriter, error) {
	if path == "" {
		return nil, nil
	}
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024
	}
	if maxBackups < 1 {
		maxBackups = 3
	}
	if perm == 0 {
		perm = 0644
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, perm)
	if err != nil {
		return nil, err
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	return &RotateWriter{
		path:       path,
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
		filePerm:   perm,
		file:       f,
		size:       info.Size(),
	}, nil
}

func (w *RotateWriter) Write(p []byte) (int, error) {
	if w == nil {
		return len(p), nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return 0, fmt.Errorf("rotate writer is closed")
	}

	if w.maxBytes > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotateWriter) Close() error {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}

	err := w.file.Close()
	w.file = nil
	return err
}

func (w *RotateWriter) rotate() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}

	// 刪掉最舊的
	oldest := fmt.Sprintf("%s.%d", w.path, w.maxBackups)
	_ = os.Remove(oldest)

	// 往後搬移
	for i := w.maxBackups - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", w.path, i)
		dst := fmt.Sprintf("%s.%d", w.path, i+1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}

	// 現有主檔 -> .1
	if _, err := os.Stat(w.path); err == nil {
		if err := os.Rename(w.path, fmt.Sprintf("%s.1", w.path)); err != nil {
			return err
		}
	}

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, w.filePerm)
	if err != nil {
		return err
	}

	w.file = f
	w.size = 0
	return nil
}
