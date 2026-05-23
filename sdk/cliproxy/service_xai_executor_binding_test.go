package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestEnsureExecutorsForAuth_XAIBindsIndependentExecutor(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := &coreauth.Auth{
		ID:       "xai-auth-1",
		Provider: "xai",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}

	service.ensureExecutorsForAuth(auth)
	resolved, ok := service.coreManager.Executor("xai")
	if !ok || resolved == nil {
		t.Fatal("expected xai executor after bind")
	}
	if _, isXAI := resolved.(*executor.XAIExecutor); !isXAI {
		t.Fatalf("executor type = %T, want *executor.XAIExecutor", resolved)
	}
	if _, isCodex := resolved.(*executor.CodexAutoExecutor); isCodex {
		t.Fatal("xai must not bind the codex auto executor")
	}
}

func TestEnsureExecutorsForAuth_TraeBindsIndependentExecutor(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := &coreauth.Auth{
		ID:       "trae-auth-1",
		Provider: "trae",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}

	service.ensureExecutorsForAuth(auth)
	resolved, ok := service.coreManager.Executor("trae")
	if !ok || resolved == nil {
		t.Fatal("expected trae executor after bind")
	}
	if _, isTrae := resolved.(*executor.TraeExecutor); !isTrae {
		t.Fatalf("executor type = %T, want *executor.TraeExecutor", resolved)
	}
}

func TestRegisterModelsForAuth_TraeSkipsWhenDynamicFetchUnavailable(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "trae-auth-models",
		Provider: "trae",
		Status:   coreauth.StatusActive,
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := registry.GetGlobalRegistry().GetModelsForClient(auth.ID)
	if len(models) == 0 {
		return
	}
	t.Fatalf("expected no Trae models without a dynamic fetcher, got %v", models)
}
