package images

import (
	"io"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if Has(dir, "nginx:alpine") {
		t.Fatal("present")
	}
	if err := Save(dir, "nginx:alpine", strings.NewReader("blob")); err != nil {
		t.Fatal(err)
	}
	if !Has(dir, "nginx:alpine") {
		t.Fatal("missing")
	}
	f, err := Open(dir, "nginx:alpine")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "blob" {
		t.Fatalf("%q %v", b, err)
	}
}
