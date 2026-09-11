//go:build !(linux && amd64)

package lingma

import "errors"

func hasEmbeddedLingma() bool {
	return false
}

func extractEmbeddedLingma(destDir string) (string, error) {
	return "", errors.New("no embedded Lingma binary for this architecture")
}
