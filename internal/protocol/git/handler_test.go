package git_test

import (
	"testing"

	"github.com/dxc-internal/omnirepo/internal/config"
	gitpkg "github.com/dxc-internal/omnirepo/internal/protocol/git"
	"github.com/dxc-internal/omnirepo/internal/protocol/git/gitkit"
	"github.com/dxc-internal/omnirepo/internal/protocol/git/gogit"
)

// --- Test 6: Backend selection from config ---

func TestBackendSelection_Gogit(t *testing.T) {
	cfg := config.Defaults()
	cfg.Server.GitBackend = "gogit"

	backend := gitpkg.SelectBackend(cfg)
	if backend.BackendName() != "gogit" {
		t.Fatalf("backend=%q want gogit", backend.BackendName())
	}
}

func TestBackendSelection_Gitkit(t *testing.T) {
	cfg := config.Defaults()
	cfg.Server.GitBackend = "gitkit"

	backend := gitpkg.SelectBackend(cfg)
	if backend.BackendName() != "gitkit" {
		t.Fatalf("backend=%q want gitkit", backend.BackendName())
	}
}

func TestBackendSelection_DefaultIsGogit(t *testing.T) {
	cfg := config.Defaults()
	// Default config.Server.GitBackend = "gogit"
	backend := gitpkg.SelectBackend(cfg)
	if _, ok := backend.(*gogit.Server); !ok {
		t.Fatalf("default backend is not *gogit.Server: %T", backend)
	}
}

func TestBackendSelection_GitkitReturnsGitkitType(t *testing.T) {
	cfg := config.Defaults()
	cfg.Server.GitBackend = "gitkit"
	backend := gitpkg.SelectBackend(cfg)
	if _, ok := backend.(*gitkit.Server); !ok {
		t.Fatalf("gitkit backend is not *gitkit.Server: %T", backend)
	}
}
