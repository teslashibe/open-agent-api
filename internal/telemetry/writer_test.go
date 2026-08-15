package telemetry

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterMirrorsAndPersists(t *testing.T) {
	var stdout bytes.Buffer
	path := filepath.Join(t.TempDir(), "nested", "telemetry.log")
	writer, err := New(&stdout, path, 1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	if _, err := writer.Write([]byte("request completed\n")); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "request completed\n" {
		t.Fatalf("stdout = %q", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); got != "request completed\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestWriterRotatesAndBoundsBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.log")
	writer, err := New(&bytes.Buffer{}, path, 8, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"first\n", "second\n", "third\n", "fourth\n"} {
		if _, err := writer.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	for _, suffix := range []string{"", ".1", ".2"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Fatalf("expected %s: %v", path+suffix, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("unexpected third backup: %v", err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), "fourth") {
		t.Fatalf("current file = %q", current)
	}
}

func TestNewRejectsUnwritablePath(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(&bytes.Buffer{}, filepath.Join(parent, "telemetry.log"), 8, 1); err == nil {
		t.Fatal("expected initialization error")
	}
}
