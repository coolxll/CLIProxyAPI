package lingma

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func decompressGzToFile(gzData []byte, targetPath string, perm os.FileMode) error {
	if len(gzData) == 0 {
		return fmt.Errorf("empty gzip data")
	}

	gr, err := gzip.NewReader(bytes.NewReader(gzData))
	if err != nil {
		return fmt.Errorf("create gzip reader: %w", err)
	}
	defer gr.Close()

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create target directory: %w", err)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(targetPath), ".lingma-extract-*")
	if err != nil {
		return fmt.Errorf("create temp file for decompression: %w", err)
	}
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
	}()

	if _, err := io.Copy(tmpFile, gr); err != nil {
		return fmt.Errorf("decompress data: %w", err)
	}
	if err := tmpFile.Chmod(perm); err != nil {
		return fmt.Errorf("chmod decompressed file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close decompressed file: %w", err)
	}

	if err := os.Rename(tmpFile.Name(), targetPath); err != nil {
		// Fallback for cross-device renames or Windows replace
		_ = os.Remove(targetPath)
		return os.Rename(tmpFile.Name(), targetPath)
	}
	return nil
}
