package providerplugins_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/lingma"
	"github.com/router-for-me/CLIProxyAPI/v7/provider-plugins/internal/trae"
	"gopkg.in/yaml.v3"
)

func TestReleaseManifestsMatchPluginVersions(t *testing.T) {
	tests := []struct {
		id      string
		version string
	}{
		{id: lingma.ProviderID, version: lingma.Version},
		{id: trae.ProviderID, version: trae.Version},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			path := filepath.Join("manifests", test.id+".yaml")
			data, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatalf("read manifest: %v", errRead)
			}
			var manifest pluginstore.Manifest
			if errUnmarshal := yaml.Unmarshal(data, &manifest); errUnmarshal != nil {
				t.Fatalf("decode manifest: %v", errUnmarshal)
			}
			if errValidate := manifest.Validate(); errValidate != nil {
				t.Fatalf("validate manifest: %v", errValidate)
			}
			if manifest.ID != test.id || manifest.Version != test.version {
				t.Fatalf("manifest identity = %s@%s, want %s@%s", manifest.ID, manifest.Version, test.id, test.version)
			}
		})
	}
}
