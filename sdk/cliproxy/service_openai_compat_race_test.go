package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestPutOpenAICompatConcurrentWithAuthFieldsPatch_Issue6559(t *testing.T) {
	authDir := t.TempDir()
	fileName := "codex-race-6559.json"
	filePath := filepath.Join(authDir, fileName)

	teamIDToken := "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6ICJ1c2VyQGV4YW1wbGUuY29tIiwgImh0dHBzOi8vYXBpLm9wZW5haS5jb20vYXV0aCI6IHsiY2hhdGdwdF9wbGFuX3R5cGUiOiAidGVhbSIsICJjaGF0Z3B0X2FjY291bnRfaWQiOiAiYWNjLTEyMyJ9fQ.sig"
	initialContent := fmt.Sprintf(`{"type":"codex","email":"user@example.com","access_token":"token-initial","id_token":%q}`, teamIDToken)
	if errWrite := os.WriteFile(filePath, []byte(initialContent), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	secretKey := "test-secret"
	hashedSecret, errHash := bcrypt.GenerateFromPassword([]byte(secretKey), bcrypt.MinCost)
	if errHash != nil {
		t.Fatalf("bcrypt hash failed: %v", errHash)
	}

	configPath := filepath.Join(authDir, "config.yaml")
	initialYAML := fmt.Sprintf("port: 8317\nauth-dir: %q\nremote-management:\n  allow-remote: true\n  secret-key: %q\n", filepath.ToSlash(authDir), string(hashedSecret))
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("write config file: %v", errWrite)
	}

	reg := internalregistry.GetGlobalRegistry()
	authID := fileName
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	cfg := &config.Config{
		AuthDir: authDir,
		RemoteManagement: config.RemoteManagement{
			SecretKey:   string(hashedSecret),
			AllowRemote: true,
		},
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "compat-initial",
				BaseURL: "https://compat.example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-initial"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "model-initial", Alias: "alias-initial"},
				},
			},
		},
	}

	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() failed: %v", errBuild)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	watcherWrapper, errWatcher := service.watcherFactory(configPath, authDir, func(newCfg *config.Config) {
		service.applyWatcherConfigUpdate(newCfg)
	})
	if errWatcher != nil {
		t.Fatalf("watcherFactory failed: %v", errWatcher)
	}
	service.watcher = watcherWrapper
	queueCtx, stopQueue := context.WithCancel(ctx)
	service.authUpdates = make(chan watcher.AuthUpdate, 256)
	service.authQueueStop = stopQueue
	queueDone := make(chan struct{})
	go func() {
		defer close(queueDone)
		service.consumeAuthUpdates(queueCtx)
	}()
	watcherWrapper.SetAuthUpdateQueue(service.authUpdates)
	watcherWrapper.SetConfig(service.cfg)
	t.Cleanup(func() {
		if errStop := watcherWrapper.Stop(); errStop != nil {
			t.Errorf("stop watcher: %v", errStop)
		}
		stopQueue()
		<-queueDone
		for _, a := range service.coreManager.List() {
			if a != nil {
				reg.UnregisterClient(a.ID)
			}
		}
	})

	finalReloadDone := make(chan struct{})
	var finalReloadOnce sync.Once
	options := append([]api.ServerOption(nil), service.serverOptions...)
	options = append(options, api.WithConfigReloadHook(func(_ context.Context, snapshot *config.Config) {
		service.reloadConfigFromWatcher()
		if len(snapshot.OpenAICompatibility) == 1 && snapshot.OpenAICompatibility[0].Name == "compat-9" {
			finalReloadOnce.Do(func() { close(finalReloadDone) })
		}
	}))
	server := api.NewServer(service.cfg, service.coreManager, service.accessManager, service.configPath, options...)
	if server == nil {
		t.Fatal("NewServer() returned nil")
	}
	service.server = server
	handler := server.Handler()
	service.coreManager.RegisterExecutor(&syncTestExecutor{})

	auth := &coreauth.Auth{
		ID:       authID,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "codex",
			"email":        "user@example.com",
			"access_token": "token-initial",
			"id_token":     teamIDToken,
		},
		Attributes: map[string]string{
			"plan_type": "team",
			"path":      filePath,
		},
	}
	if _, errRegister := service.coreManager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	service.registerConfigAPIKeyAuths(ctx, service.cfg)

	const iterations = 10
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			putBody := fmt.Sprintf(`[{"name":"compat-%d","base-url":"https://compat.example.com/v1","api-key-entries":[{"api-key":"sk-%d"}],"models":[{"name":"model-%d","alias":"alias-%d"}]}]`, i, i, i, i)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/v0/management/openai-compatibility", strings.NewReader(putBody))
			req.Header.Set("Authorization", "Bearer "+secretKey)
			req.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("PUT openai-compatibility failed: status=%d body=%s", rec.Code, rec.Body.String())
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			patchBody := fmt.Sprintf(`{"name":%q,"prefix":"pfx-%d"}`, fileName, i)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(patchBody))
			req.Header.Set("Authorization", "Bearer "+secretKey)
			req.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("PATCH auth-files/fields failed: status=%d body=%s", rec.Code, rec.Body.String())
				return
			}
		}
	}()

	close(start)
	wg.Wait()
	// A successful management response precedes its asynchronous config reload.
	select {
	case <-finalReloadDone:
	case <-time.After(5 * time.Second):
		t.Fatal("final management config reload did not complete")
	}

	// Ensure the final persisted configuration is reloaded through the watcher pipeline
	// and verify both the patched auth prefix and final openai-compatibility model are registered.
	service.reloadConfigFromWatcher()
	service.registerConfigAPIKeyAuths(ctx, service.currentConfig())

	finalPrefixModel := fmt.Sprintf("pfx-%d/gpt-6-astra", iterations-1)
	finalCompatAlias := fmt.Sprintf("alias-%d", iterations-1)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		foundCompatModel := false
		for _, a := range service.coreManager.List() {
			if a != nil && reg.ClientSupportsModel(a.ID, finalCompatAlias) {
				foundCompatModel = true
				break
			}
		}
		if foundCompatModel && reg.ClientSupportsModel(authID, finalPrefixModel) {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("final registrations did not converge: prefix=%q alias=%q found=%t", finalPrefixModel, finalCompatAlias, foundCompatModel)
		}
	}
}

func TestPutOpenAICompatConcurrentWithConfigReloadAndAuthUpdate_Issue6559(t *testing.T) {
	authDir := t.TempDir()
	fileName := "codex-reload-race-6559.json"
	filePath := filepath.Join(authDir, fileName)

	teamIDToken := "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6ICJ1c2VyQGV4YW1wbGUuY29tIiwgImh0dHBzOi8vYXBpLm9wZW5haS5jb20vYXV0aCI6IHsiY2hhdGdwdF9wbGFuX3R5cGUiOiAidGVhbSIsICJjaGF0Z3B0X2FjY291bnRfaWQiOiAiYWNjLTEyMyJ9fQ.sig"
	initialContent := fmt.Sprintf(`{"type":"codex","email":"user@example.com","access_token":"token-initial","id_token":%q}`, teamIDToken)
	if errWrite := os.WriteFile(filePath, []byte(initialContent), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	configPath := filepath.Join(authDir, "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); errWrite != nil {
		t.Fatalf("write config file: %v", errWrite)
	}

	reg := internalregistry.GetGlobalRegistry()
	authID := fileName
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	secretKey := "test-secret"
	hashedSecret, errHash := bcrypt.GenerateFromPassword([]byte(secretKey), bcrypt.MinCost)
	if errHash != nil {
		t.Fatalf("bcrypt hash failed: %v", errHash)
	}

	buildCfg := func(iter int) *config.Config {
		return &config.Config{
			AuthDir: authDir,
			RemoteManagement: config.RemoteManagement{
				SecretKey:   string(hashedSecret),
				AllowRemote: true,
			},
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    fmt.Sprintf("compat-reload-%d", iter),
					BaseURL: "https://compat.example.com/v1",
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: fmt.Sprintf("sk-reload-%d", iter)},
					},
					Models: []config.OpenAICompatibilityModel{
						{Name: fmt.Sprintf("model-reload-%d", iter), Alias: fmt.Sprintf("alias-reload-%d", iter)},
					},
				},
			},
		}
	}

	service, errBuild := NewBuilder().
		WithConfig(buildCfg(0)).
		WithConfigPath(configPath).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() failed: %v", errBuild)
	}
	t.Cleanup(func() {
		for _, a := range service.coreManager.List() {
			if a != nil {
				reg.UnregisterClient(a.ID)
			}
		}
	})

	server := api.NewServer(service.cfg, service.coreManager, service.accessManager, service.configPath, service.serverOptions...)
	if server == nil {
		t.Fatal("NewServer() returned nil")
	}
	service.server = server
	handler := server.Handler()
	service.coreManager.RegisterExecutor(&syncTestExecutor{})

	auth := &coreauth.Auth{
		ID:       authID,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "codex",
			"email":        "user@example.com",
			"access_token": "token-initial",
			"id_token":     teamIDToken,
		},
		Attributes: map[string]string{
			"plan_type": "team",
			"path":      filePath,
		},
	}
	if _, errRegister := service.coreManager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	const iterations = 10
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			putBody := fmt.Sprintf(`[{"name":"compat-%d","base-url":"https://compat.example.com/v1","api-key-entries":[{"api-key":"sk-%d"}],"models":[{"name":"model-%d","alias":"alias-%d"}]}]`, i, i, i, i)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/v0/management/openai-compatibility", strings.NewReader(putBody))
			req.Header.Set("Authorization", "Bearer "+secretKey)
			req.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("PUT openai-compatibility failed: status=%d body=%s", rec.Code, rec.Body.String())
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			patchBody := fmt.Sprintf(`{"name":%q,"prefix":"pfx-%d"}`, fileName, i)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(patchBody))
			req.Header.Set("Authorization", "Bearer "+secretKey)
			req.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("PATCH auth-files/fields failed: status=%d body=%s", rec.Code, rec.Body.String())
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			service.applyConfigUpdateWithAuthSynthesis(context.Background(), buildCfg(i+1), true)
		}
	}()

	close(start)
	wg.Wait()

	finalReloadAlias := fmt.Sprintf("alias-reload-%d", iterations)
	foundReloadModel := false
	for _, a := range service.coreManager.List() {
		if a != nil && reg.ClientSupportsModel(a.ID, finalReloadAlias) {
			foundReloadModel = true
			break
		}
	}
	if !foundReloadModel {
		t.Fatalf("expected registered openai-compatibility auth to support final reloaded model %q", finalReloadAlias)
	}
}
