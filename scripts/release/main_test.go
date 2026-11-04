package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionsRejectUnsafeAndInvalidInput(t *testing.T) {
	for _, v := range []string{"v0.1.0", "v1.2.3-rc.1", "v1.2.3+build.1"} {
		if !validVersion(v) {
			t.Errorf("rejected %q", v)
		}
	}
	for _, v := range []string{"", "dev", "1.2.3", "v01.2.3", "v1.2.3-01", "v1.2.3\n", "v1.2.3/../../x", "v1.2.3 -X injected", "$(id)"} {
		if validVersion(v) {
			t.Errorf("accepted %q", v)
		}
	}
}

func TestArchiveContentsModesAndDeterminism(t *testing.T) {
	files := []archiveFile{{"up", 0o755, []byte("binary")}, {"LICENSE", 0o644, []byte("notice")}}
	var previous []byte
	for i := range 2 {
		path := filepath.Join(t.TempDir(), "archive.tar.gz")
		if err := writeArchive(path, files); err != nil {
			t.Fatal(err)
		}
		if err := writeArchive(path, files); err == nil {
			t.Fatal("overwrote an existing archive")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && !bytes.Equal(previous, data) {
			t.Fatal("archive metadata is nondeterministic")
		}
		previous = data
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(zr)
		for _, want := range files {
			h, err := tr.Next()
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			if h.Name != want.name || h.Mode != want.mode || !bytes.Equal(body, want.data) {
				t.Fatalf("unexpected archive entry: %+v", h)
			}
		}
		if _, err := tr.Next(); err != io.EOF {
			t.Fatalf("extra archive entries: %v", err)
		}
		if err := zr.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
