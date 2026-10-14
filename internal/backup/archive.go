package backup

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
)

// TarGz 把 src(文件或目录)打包为 dst。目录时保留相对路径,仅归档普通文件。
func TarGz(src, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return writeFile(tw, src, filepath.Base(src))
	}
	root := filepath.Clean(src)
	return filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.Mode().IsDir() || !fi.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return writeFile(tw, path, filepath.ToSlash(rel))
	})
}

func writeFile(tw *tar.Writer, path, name string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	hdr.Format = tar.FormatPAX
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}
