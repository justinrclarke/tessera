package volume

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func Restore(source io.Reader, destination string, maxBytes int64) (result error) {
	if maxBytes <= 0 {
		return fmt.Errorf("restore byte limit must be positive")
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return fmt.Errorf("restore requires a new destination directory: %w", err)
	}
	defer func() {
		if result != nil {
			_ = os.RemoveAll(destination)
		}
	}()
	reader := tar.NewReader(source)
	root := ""
	type directoryMode struct {
		path string
		mode os.FileMode
	}
	var directories []directoryMode
	var used int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
			return fmt.Errorf("unsafe archive path %q", header.Name)
		}
		if root == "" {
			root = strings.SplitN(name, "/", 2)[0]
		}
		if name != root && !strings.HasPrefix(name, root+"/") {
			return fmt.Errorf("archive must contain one storage directory")
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(name, root), "/")
		target := filepath.Join(destination, filepath.FromSlash(relative))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			directories = append(directories, directoryMode{target, os.FileMode(header.Mode) & 0777})
		case tar.TypeReg, tar.TypeRegA:
			if relative == "" || header.Size < 0 || header.Size > maxBytes-used {
				return fmt.Errorf("invalid archive entry or restore byte limit exceeded")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode)&0777)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, reader)
			syncErr := file.Sync()
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if syncErr != nil {
				return syncErr
			}
			if closeErr != nil {
				return closeErr
			}
			used += header.Size
		default:
			return fmt.Errorf("restore supports regular files and directories; entry %q has unsupported type %d", header.Name, header.Typeflag)
		}
	}
	if root == "" {
		return fmt.Errorf("empty storage archive")
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := os.Chmod(directories[i].path, directories[i].mode); err != nil {
			return err
		}
	}
	return nil
}
