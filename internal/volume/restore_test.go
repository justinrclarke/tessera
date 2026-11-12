package volume

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreRefusesTraversalLinksOverwritesAndOversize(t *testing.T) {
	for _, name := range []string{"data/file", "../escape", "data/../../escape", "/absolute", "data/link", "data/large"} {
		t.Run(name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			header := &tar.Header{Name: name, Mode: 0600, Size: 4, Typeflag: tar.TypeReg}
			limit := int64(10)
			if name == "data/link" {
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "../../outside"
				header.Size = 0
			}
			if name == "data/large" {
				limit = 3
			}
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if header.Size > 0 {
				writer.Write([]byte("data"))
			}
			writer.Close()
			destination := filepath.Join(t.TempDir(), "restored")
			err := Restore(bytes.NewReader(archive.Bytes()), destination, limit)
			if name == "data/file" {
				if err != nil {
					t.Fatal(err)
				}
				if data, err := os.ReadFile(filepath.Join(destination, "file")); err != nil || string(data) != "data" {
					t.Fatalf("%q %v", data, err)
				}
				if err := Restore(bytes.NewReader(archive.Bytes()), destination, limit); err == nil {
					t.Fatal("overwrote existing destination")
				}
			} else {
				if err == nil {
					t.Fatal("unsafe archive accepted")
				}
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatal("partial restore retained")
				}
			}
		})
	}
}
