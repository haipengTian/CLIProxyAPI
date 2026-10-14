package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestReloadConfigIfChanged_DoesNotMarkConcurrentWriteAsLoaded(t *testing.T) {
	authDir := t.TempDir()
	configPath := filepath.Join(authDir, "config.yaml")
	writeConfig := func(port int) {
		t.Helper()
		content := fmt.Sprintf("port: %d\nauth-dir: %q\n", port, filepath.ToSlash(authDir))
		if errWrite := os.WriteFile(configPath, []byte(content), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	writeConfig(8317)
	var ports []int
	w := &Watcher{
		configPath: configPath,
		authDir:    authDir,
		reloadCallback: func(cfg *config.Config) {
			ports = append(ports, cfg.Port)
			if len(ports) == 1 {
				// A management save can complete while the previous reload is applying.
				writeConfig(8318)
			}
		},
	}
	w.ReloadConfigIfChanged()
	w.ReloadConfigIfChanged()
	if len(ports) != 2 || ports[0] != 8317 || ports[1] != 8318 {
		t.Fatalf("expected both saved configurations to load, got ports %v", ports)
	}
	w.ReloadConfigIfChanged()
	if len(ports) != 2 {
		t.Fatalf("unchanged configuration reloaded, got ports %v", ports)
	}
}

func TestReloadConfigIfChanged_SerializesConcurrentApplications(t *testing.T) {
	authDir := t.TempDir()
	configPath := filepath.Join(authDir, "config.yaml")
	writeConfig := func(port int) {
		t.Helper()
		content := fmt.Sprintf("port: %d\nauth-dir: %q\n", port, filepath.ToSlash(authDir))
		if errWrite := os.WriteFile(configPath, []byte(content), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	writeConfig(8317)
	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var ports []int
	active, maxActive := 0, 0
	w := &Watcher{configPath: configPath, authDir: authDir}
	w.reloadCallback = func(cfg *config.Config) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		ports = append(ports, cfg.Port)
		mu.Unlock()
		if cfg.Port == 8317 {
			close(entered)
			<-release
		}
		mu.Lock()
		active--
		mu.Unlock()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		w.ReloadConfigIfChanged()
	}()
	<-entered
	writeConfig(8318)
	started := make(chan struct{})
	go func() {
		defer wg.Done()
		close(started)
		w.ReloadConfigIfChanged()
	}()
	<-started
	close(release)
	wg.Wait()
	if maxActive != 1 || len(ports) != 2 || ports[0] != 8317 || ports[1] != 8318 {
		t.Fatalf("expected serialized applications [8317 8318], max active=%d ports=%v", maxActive, ports)
	}
}
