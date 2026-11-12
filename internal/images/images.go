package images

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tessera/internal/host"
)

const DefaultMaxBytes int64 = 10 << 30
const DefaultMinFree int64 = 1 << 30

var ErrCapacity = errors.New("image cache capacity exceeded")
var writes sync.Mutex

type Limits struct {
	MaxBytes int64
	MinFree  int64
	free     func(string) int64
}

func Defaults() Limits { return Limits{MaxBytes: DefaultMaxBytes, MinFree: DefaultMinFree} }

func File(dir, ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return filepath.Join(dir, "images", hex.EncodeToString(sum[:])+".tar")
}

func Has(dir, ref string) bool {
	st, err := os.Stat(File(dir, ref))
	return err == nil && !st.IsDir()
}

func Save(dir, ref string, r io.Reader) error {
	return SaveLimited(dir, ref, r, Defaults())
}

func SaveLimited(dir, ref string, r io.Reader, limits Limits) error {
	writes.Lock()
	defer writes.Unlock()
	if limits.MaxBytes <= 0 || limits.MinFree < 0 {
		return fmt.Errorf("invalid image cache limits")
	}
	path := File(dir, ref)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	used, err := Usage(dir)
	if err != nil {
		return err
	}
	remaining := limits.MaxBytes - used
	if remaining < 0 {
		return ErrCapacity
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".image-*.partial")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	buf := make([]byte, 1<<20)
	free := limits.free
	if free == nil {
		free = func(path string) int64 {
			_, _, _, available := host.Resources(path)
			return available
		}
	}
	for {
		readLimit := int64(len(buf))
		if remaining < readLimit {
			readLimit = remaining + 1
		}
		n, readErr := r.Read(buf[:readLimit])
		if n > 0 {
			if int64(n) > remaining || free(dir)-int64(n) < limits.MinFree {
				return fmt.Errorf("%w: limit %d bytes, minimum free %d bytes", ErrCapacity, limits.MaxBytes, limits.MinFree)
			}
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			remaining -= int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	closeErr := f.Close()
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}

func Open(dir, ref string) (*os.File, error) {
	f, err := os.Open(File(dir, ref))
	if err == nil {
		now := time.Now()
		_ = os.Chtimes(File(dir, ref), now, now)
	}
	return f, err
}

func Usage(dir string) (int64, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "images"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var size int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
	}
	return size, nil
}

func Prune(dir string, keep map[string]bool, before time.Time) error {
	writes.Lock()
	defer writes.Unlock()
	paths := map[string]bool{}
	for ref := range keep {
		paths[filepath.Base(File(dir, ref))] = true
	}
	entries, err := os.ReadDir(filepath.Join(dir, "images"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if paths[name] || !(strings.HasSuffix(name, ".tar") || strings.HasPrefix(name, ".image-") && strings.HasSuffix(name, ".partial")) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && info.ModTime().Before(before) {
			if err := os.Remove(filepath.Join(dir, "images", name)); err != nil {
				return err
			}
		}
	}
	return nil
}
