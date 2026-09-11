package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/lingma"
)

func main() {
	destDir := flag.String("dest", "", "destination directory for Lingma binary (defaults to ~/.lingma/bin/<version>/<platform>)")
	flag.Parse()

	fmt.Println("Downloading official Lingma service binary from VSIX...")
	path, err := lingma.DownloadLingmaServiceBinary(*destDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error downloading Lingma binary: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Successfully installed Lingma service binary at: %s\n", path)
}
