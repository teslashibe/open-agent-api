package telemetry

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Writer mirrors telemetry to stdout and an optional bounded, rotating file.
type Writer struct {
	mu      sync.Mutex
	stdout  io.Writer
	path    string
	maxSize int64
	backups int
	file    *os.File
	size    int64
}

func New(stdout io.Writer, path string, maxSize int64, backups int) (*Writer, error) {
	w := &Writer{stdout: stdout, path: path, maxSize: maxSize, backups: backups}
	if path == "" {
		return w, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create telemetry directory: %w", err)
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, stdoutErr := w.stdout.Write(p)
	if w.file == nil {
		return n, stdoutErr
	}
	if w.maxSize > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return n, fmt.Errorf("rotate telemetry file: %w", err)
		}
	}
	fileN, fileErr := w.file.Write(p)
	w.size += int64(fileN)
	if stdoutErr != nil {
		return n, stdoutErr
	}
	if fileErr != nil {
		return n, fmt.Errorf("write telemetry file: %w", fileErr)
	}
	return n, nil
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	return w.file.Close()
}

func (w *Writer) open() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("open telemetry file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("stat telemetry file: %w", err)
	}
	w.file = file
	w.size = info.Size()
	return nil
}

func (w *Writer) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	if w.backups > 0 {
		_ = os.Remove(fmt.Sprintf("%s.%d", w.path, w.backups))
		for i := w.backups - 1; i >= 1; i-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
		}
		if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.open()
}
