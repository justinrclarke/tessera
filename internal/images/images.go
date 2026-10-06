package images

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
)

func File(dir, ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return filepath.Join(dir, "images", hex.EncodeToString(sum[:])+".tar")
}

func Has(dir, ref string) bool {
	st, err := os.Stat(File(dir, ref))
	return err == nil && !st.IsDir()
}

func Save(dir, ref string, r io.Reader) error {
	path := File(dir, ref)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".image-*.partial")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}

func Open(dir, ref string) (*os.File, error) {
	return os.Open(File(dir, ref))
}
