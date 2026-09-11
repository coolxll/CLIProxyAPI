package lingma

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultLingmaVSIXVersion = "2.5.20"
	DefaultLingmaVSIXURL     = "https://Alibaba-Cloud.gallery.vsassets.io/_apis/public/gallery/publisher/Alibaba-Cloud/extension/tongyi-lingma/" + DefaultLingmaVSIXVersion + "/assetbyname/Microsoft.VisualStudio.Services.VSIXPackage"
)

// LingmaBinaryResolver locates or downloads the official Lingma service binary.
type LingmaBinaryResolver struct {
	GOOS     string
	GOARCH   string
	HomeDir  string
	Env      func(string) string
	LookPath func(string) (string, error)
	Stat     func(string) (os.FileInfo, error)
}

func NewLingmaBinaryResolver() LingmaBinaryResolver {
	home, _ := os.UserHomeDir()
	return LingmaBinaryResolver{
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
		HomeDir:  home,
		Env:      os.Getenv,
		LookPath: exec.LookPath,
		Stat:     os.Stat,
	}
}

// FindLingmaServiceBinary searches for an existing official Lingma service binary.
func FindLingmaServiceBinary() (string, error) {
	return NewLingmaBinaryResolver().Resolve()
}

// EnsureLingmaServiceBinary finds an existing binary, or automatically downloads it from VSIX.
func EnsureLingmaServiceBinary() (string, error) {
	if bin, err := FindLingmaServiceBinary(); err == nil && bin != "" {
		return bin, nil
	}
	return DownloadLingmaServiceBinary("")
}

func (r LingmaBinaryResolver) Resolve() (string, error) {
	env := r.Env
	if env == nil {
		env = os.Getenv
	}
	stat := r.Stat
	if stat == nil {
		stat = os.Stat
	}
	lookPath := r.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	// 1. Explicit environment override
	if configured := strings.TrimSpace(env("LINGMA_SERVICE_BINARY")); configured != "" {
		if r.usable(stat, configured) {
			return configured, nil
		}
		return "", fmt.Errorf("LINGMA_SERVICE_BINARY does not point to a usable binary: %s", configured)
	}

	// 2. Well-known application and cache candidates
	for _, candidate := range r.candidates(env) {
		if r.usable(stat, candidate) {
			return candidate, nil
		}
	}

	// 3. System PATH lookup
	for _, name := range r.pathNames() {
		if candidate, err := lookPath(name); err == nil && r.usable(stat, candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("Lingma service binary not found for %s/%s; install Lingma or set LINGMA_SERVICE_BINARY", r.GOOS, r.GOARCH)
}

func (r LingmaBinaryResolver) candidates(env func(string) string) []string {
	home := r.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	platforms := r.platformDirectories()
	var candidates []string

	if r.GOOS == "darwin" {
		for _, root := range []string{"/Applications/Lingma.app", filepath.Join(home, "Applications", "Lingma.app")} {
			for _, platform := range platforms {
				candidates = append(candidates, filepath.Join(root, "Contents", "Resources", "app", "resources", "bin", platform, "Lingma"))
			}
		}
	} else if r.GOOS == "windows" {
		roots := []string{}
		if local := strings.TrimSpace(env("LOCALAPPDATA")); local != "" {
			roots = append(roots, filepath.Join(local, "Programs", "Lingma"))
		}
		if programFiles := strings.TrimSpace(env("ProgramFiles")); programFiles != "" {
			roots = append(roots, filepath.Join(programFiles, "Lingma"))
		}
		if len(roots) == 0 {
			roots = append(roots, filepath.Join(home, "AppData", "Local", "Programs", "Lingma"))
		}
		for _, root := range roots {
			for _, platform := range platforms {
				candidates = append(candidates, filepath.Join(root, "resources", "app", "resources", "bin", platform, "Lingma.exe"))
			}
		}
	} else if r.GOOS == "linux" {
		for _, platform := range platforms {
			candidates = append(candidates, filepath.Join("/usr/local/bin/Lingma"))
			candidates = append(candidates, filepath.Join(home, ".config", "lingma", "bin", platform, "Lingma"))
		}
	}

	// ~/.lingma/bin/<version>/<platform>/Lingma
	binRoot := filepath.Join(home, ".lingma", "bin")
	entries, _ := os.ReadDir(binRoot)
	sort.SliceStable(entries, func(i, j int) bool {
		return compareLingmaVersions(entries[i].Name(), entries[j].Name()) > 0
	})
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		for _, platform := range platforms {
			name := "Lingma"
			if r.GOOS == "windows" {
				name = "Lingma.exe"
			}
			candidates = append(candidates, filepath.Join(binRoot, entry.Name(), platform, name))
		}
	}

	return candidates
}

func (r LingmaBinaryResolver) platformDirectories() []string {
	if r.GOOS == "windows" {
		if r.GOARCH == "amd64" {
			return []string{"x86_64_windows"}
		}
		return []string{"x86_64_windows"}
	}
	if r.GOOS == "darwin" {
		if r.GOARCH == "arm64" {
			return []string{"aarch64_darwin", "x86_64_darwin"}
		}
		return []string{"x86_64_darwin"}
	}
	// linux
	if r.GOARCH == "arm64" {
		return []string{"aarch64_linux", "x86_64_linux"}
	}
	return []string{"x86_64_linux"}
}

func (r LingmaBinaryResolver) pathNames() []string {
	if r.GOOS == "windows" {
		return []string{"Lingma.exe", "Lingma"}
	}
	return []string{"Lingma"}
}

func (r LingmaBinaryResolver) usable(stat func(string) (os.FileInfo, error), path string) bool {
	info, err := stat(path)
	if err != nil || info == nil || info.IsDir() {
		return false
	}
	if r.GOOS == "windows" {
		return strings.EqualFold(filepath.Ext(path), ".exe")
	}
	return info.Mode()&0o111 != 0
}

func compareLingmaVersions(a, b string) int {
	a = strings.TrimPrefix(strings.ToLower(a), "v")
	b = strings.TrimPrefix(strings.ToLower(b), "v")
	ap := strings.FieldsFunc(a, func(r rune) bool { return r == '.' || r == '-' || r == '_' })
	bp := strings.FieldsFunc(b, func(r rune) bool { return r == '.' || r == '-' || r == '_' })
	for i := 0; i < len(ap) && i < len(bp); i++ {
		an, aerr := strconv.Atoi(ap[i])
		bn, berr := strconv.Atoi(bp[i])
		if aerr == nil && berr == nil && an != bn {
			if an > bn {
				return 1
			}
			return -1
		}
		if ap[i] != bp[i] {
			if ap[i] > bp[i] {
				return 1
			}
			return -1
		}
	}
	if len(ap) != len(bp) {
		if len(ap) > len(bp) {
			return 1
		}
		return -1
	}
	return 0
}

// DownloadLingmaServiceBinary downloads the official Lingma VSIX extension and
// extracts the official service binary for the current OS/Arch.
func DownloadLingmaServiceBinary(destDir string) (string, error) {
	home, _ := os.UserHomeDir()
	if destDir == "" {
		destDir = filepath.Join(home, ".lingma", "bin", DefaultLingmaVSIXVersion)
	}

	platforms := NewLingmaBinaryResolver().platformDirectories()
	if len(platforms) == 0 {
		return "", fmt.Errorf("unsupported platform for Lingma: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	primaryPlatform := platforms[0]
	binaryName := "Lingma"
	if runtime.GOOS == "windows" {
		binaryName = "Lingma.exe"
	}
	finalPath := filepath.Join(destDir, primaryPlatform, binaryName)
	if info, err := os.Stat(finalPath); err == nil && info.Mode()&0o111 != 0 {
		return finalPath, nil
	}

	// Download VSIX to temporary file
	tmpFile, err := os.CreateTemp("", "lingma-vsix-*.zip")
	if err != nil {
		return "", fmt.Errorf("create temporary vsix file: %w", err)
	}
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
	}()

	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, DefaultLingmaVSIXURL, nil)
	if err != nil {
		return "", fmt.Errorf("create vsix request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; CLIProxyAPI-LingmaPlugin/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download lingma vsix: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download lingma vsix HTTP %d", resp.StatusCode)
	}

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return "", fmt.Errorf("save lingma vsix: %w", err)
	}
	_ = tmpFile.Close()

	// Extract binary from zip
	zr, err := zip.OpenReader(tmpFile.Name())
	if err != nil {
		return "", fmt.Errorf("open lingma vsix zip: %w", err)
	}
	defer zr.Close()

	// Search for binary matching one of the platform directories
	var matchedFile *zip.File
	for _, p := range platforms {
		expectedSuffix := "resources/bin/" + p + "/" + binaryName
		for _, f := range zr.File {
			cleanName := strings.ReplaceAll(filepath.ToSlash(f.Name), "\\", "/")
			if strings.HasSuffix(cleanName, expectedSuffix) {
				matchedFile = f
				primaryPlatform = p
				finalPath = filepath.Join(destDir, primaryPlatform, binaryName)
				break
			}
		}
		if matchedFile != nil {
			break
		}
	}

	if matchedFile == nil {
		return "", fmt.Errorf("binary %s not found in Lingma VSIX for platforms %v", binaryName, platforms)
	}

	rc, err := matchedFile.Open()
	if err != nil {
		return "", fmt.Errorf("open binary in vsix zip: %w", err)
	}
	defer rc.Close()

	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return "", fmt.Errorf("create binary destination dir: %w", err)
	}

	outFile, err := os.OpenFile(finalPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("create extracted binary: %w", err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, rc); err != nil {
		return "", fmt.Errorf("extract binary data: %w", err)
	}

	return finalPath, nil
}
