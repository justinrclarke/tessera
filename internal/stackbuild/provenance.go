package stackbuild

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func (s Spec) contextDirectory(manifest string) string {
	dir := s.Context
	if dir == "" {
		dir = "."
	}
	if !filepath.IsAbs(dir) && manifest != "-" {
		dir = filepath.Join(filepath.Dir(manifest), dir)
	}
	return dir
}

func (s Spec) contextDigest(manifest string) (string, error) {
	root := s.contextDirectory(manifest)
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%o\x00%d\x00", filepath.ToSlash(rel), info.Mode(), info.Size())
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprint(hash, target)
		} else if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(hash, f)
			f.Close()
			if copyErr != nil {
				return copyErr
			}
		} else if !info.IsDir() {
			return fmt.Errorf("build context contains unsupported file %s", rel)
		}
		hash.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}
