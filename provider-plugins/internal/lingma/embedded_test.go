package lingma

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestDecompressGzToFile(t *testing.T) {
	gzPath := filepath.Join("assets", "linux_amd64", "Lingma.gz")
	data, err := os.ReadFile(gzPath)
	if err != nil {
		t.Skipf("assets/linux_amd64/Lingma.gz not found: %v", err)
	}

	destDir := t.TempDir()
	targetFile := filepath.Join(destDir, "Lingma")
	if err := decompressGzToFile(data, targetFile, 0o755); err != nil {
		t.Fatalf("decompressGzToFile failed: %v", err)
	}

	info, err := os.Stat(targetFile)
	if err != nil {
		t.Fatalf("stat decompressed file: %v", err)
	}
	if info.Size() != 84499864 {
		t.Fatalf("expected size 84499864, got %d", info.Size())
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("expected file to be executable, mode = %v", info.Mode())
	}

	// Verify SHA256 matches the original Linux binary
	decompressedBytes, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read decompressed file: %v", err)
	}
	sum := sha256.Sum256(decompressedBytes)
	hashStr := hex.EncodeToString(sum[:])
	expectedHash := "9ae9bb4d37cbe6cb7097a17b7cccd3efe928ee2d6f4074f1633b45c5e345175c"
	if hashStr != expectedHash {
		t.Fatalf("SHA256 mismatch: got %s, want %s", hashStr, expectedHash)
	}
}
