//go:build linux && amd64

package lingma

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed assets/linux_amd64/Lingma.gz
var embeddedLingmaGz []byte

//go:embed assets/linux_amd64/LingmaLocal.gz
var embeddedLingmaLocalGz []byte

func hasEmbeddedLingma() bool {
	return len(embeddedLingmaGz) > 0
}

func extractEmbeddedLingma(destDir string) (string, error) {
	if !hasEmbeddedLingma() {
		return "", fmt.Errorf("no embedded Lingma binary available for linux/amd64")
	}

	home, _ := os.UserHomeDir()
	if destDir == "" {
		destDir = filepath.Join(home, ".lingma", "bin", DefaultLingmaVSIXVersion)
	}

	platform := "x86_64_linux"
	platformDir := filepath.Join(destDir, platform)
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		return "", fmt.Errorf("create directory for embedded lingma: %w", err)
	}

	finalPath := filepath.Join(platformDir, "Lingma")
	if info, err := os.Stat(finalPath); err == nil && info.Mode()&0o111 != 0 {
		return finalPath, nil
	}

	// Extract Lingma
	if err := decompressGzToFile(embeddedLingmaGz, finalPath, 0o755); err != nil {
		return "", fmt.Errorf("extract embedded Lingma: %w", err)
	}

	// Extract LingmaLocal if present
	if len(embeddedLingmaLocalGz) > 0 {
		localPath := filepath.Join(platformDir, "LingmaLocal")
		_ = decompressGzToFile(embeddedLingmaLocalGz, localPath, 0o755)
	}

	return finalPath, nil
}
