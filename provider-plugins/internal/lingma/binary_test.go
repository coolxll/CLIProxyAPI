package lingma

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractLingmaServiceBinaryFromNestedZip(t *testing.T) {
	// Create an in-memory inner zip containing: 2.5.20/x86_64_linux/Lingma and LingmaLocal
	innerBuf := new(bytes.Buffer)
	innerZip := zip.NewWriter(innerBuf)

	files := map[string]string{
		"2.5.20/x86_64_linux/Lingma":      "fake-lingma-binary-content",
		"2.5.20/x86_64_linux/LingmaLocal": "fake-lingmalocal-content",
		"2.5.20/x86_64_darwin/Lingma":     "fake-darwin-content",
	}
	for name, content := range files {
		f, err := innerZip.Create(name)
		if err != nil {
			t.Fatalf("create inner file %s: %v", name, err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("write inner file %s: %v", name, err)
		}
	}
	if err := innerZip.Close(); err != nil {
		t.Fatalf("close inner zip: %v", err)
	}

	// Create an outer zip containing extension/dist/bin/lingma-2.5.20.zip
	tmpOuter, err := os.CreateTemp("", "test-vsix-*.zip")
	if err != nil {
		t.Fatalf("create outer tmp: %v", err)
	}
	defer func() {
		_ = tmpOuter.Close()
		_ = os.Remove(tmpOuter.Name())
	}()

	outerZip := zip.NewWriter(tmpOuter)
	innerEntry, err := outerZip.Create("extension/dist/bin/lingma-2.5.20.zip")
	if err != nil {
		t.Fatalf("create inner zip entry: %v", err)
	}
	if _, err := innerEntry.Write(innerBuf.Bytes()); err != nil {
		t.Fatalf("write inner zip entry: %v", err)
	}
	if err := outerZip.Close(); err != nil {
		t.Fatalf("close outer zip: %v", err)
	}
	_ = tmpOuter.Close()

	// Test extraction
	destDir := t.TempDir()
	platforms := []string{"x86_64_linux"}
	binaryPath, err := ExtractLingmaServiceBinaryFromVSIX(tmpOuter.Name(), destDir, platforms, "Lingma")
	if err != nil {
		t.Fatalf("ExtractLingmaServiceBinaryFromVSIX failed: %v", err)
	}

	expectedPath := filepath.Join(destDir, "x86_64_linux", "Lingma")
	if binaryPath != expectedPath {
		t.Fatalf("expected binaryPath %s, got %s", expectedPath, binaryPath)
	}

	data, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if string(data) != "fake-lingma-binary-content" {
		t.Fatalf("expected binary content 'fake-lingma-binary-content', got '%s'", string(data))
	}

	// Verify sibling file LingmaLocal was also extracted
	localData, err := os.ReadFile(filepath.Join(destDir, "x86_64_linux", "LingmaLocal"))
	if err != nil {
		t.Fatalf("read extracted sibling LingmaLocal: %v", err)
	}
	if string(localData) != "fake-lingmalocal-content" {
		t.Fatalf("expected LingmaLocal content, got '%s'", string(localData))
	}
}

func TestExtractLingmaServiceBinaryFromRealCachedVSIX(t *testing.T) {
	cachePath := "/tmp/lingma-cache/tongyi-lingma-2.5.20.vsix"
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		t.Skip("real vsix cache not found, skipping real extraction test")
	}

	destDir := t.TempDir()
	platforms := []string{"x86_64_linux"}
	binaryPath, err := ExtractLingmaServiceBinaryFromVSIX(cachePath, destDir, platforms, "Lingma")
	if err != nil {
		t.Fatalf("extract from real vsix failed: %v", err)
	}

	info, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatalf("stat extracted binary: %v", err)
	}
	if info.Size() < 50*1024*1024 {
		t.Fatalf("expected extracted binary > 50MB, got %d", info.Size())
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("expected extracted binary to be executable, got %v", info.Mode())
	}

	// Verify LingmaLocal also exists
	localPath := filepath.Join(destDir, "x86_64_linux", "LingmaLocal")
	localInfo, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat extracted LingmaLocal: %v", err)
	}
	if localInfo.Size() < 5*1024*1024 {
		t.Fatalf("expected LingmaLocal > 5MB, got %d", localInfo.Size())
	}
}
