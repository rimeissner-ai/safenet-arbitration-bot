package config

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
)

// setup isolates a test from the real environment: it changes into an empty
// working directory and points $HOME and $XDG_CONFIG_HOME at empty directories.
func setup(t *testing.T) (cwd, home, xdg string) {
	cwd, home, xdg = t.TempDir(), t.TempDir(), t.TempDir()
	t.Chdir(cwd)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	return cwd, home, xdg
}

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeSource writes a configuration file to path whose Mainnet RPC URL names
// source, so that tests can tell which file Load picked.
func writeSource(t *testing.T, path, source string) {
	t.Helper()
	write(t, path, `{"rpcs": {"1": "https://`+source+`.example"}}`)
}

// loadSource loads the configuration from path, and returns the source name
// that writeSource put in it, or an empty string for the default configuration.
func loadSource(t *testing.T, path string) string {
	t.Helper()
	config, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	url, ok := config.RPCs[1]
	if !ok {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(url, "https://"), ".example")
}

func TestLoadDefaultsWithoutFile(t *testing.T) {
	setup(t)
	if source := loadSource(t, ""); source != "" {
		t.Errorf("Load: got configuration from %q, want the default configuration", source)
	}
}

func TestLoadExplicitPath(t *testing.T) {
	cwd, _, xdg := setup(t)
	writeSource(t, filepath.Join(cwd, "arbot.config.json"), "cwd")
	writeSource(t, filepath.Join(xdg, "arbot", "config.json"), "xdg")
	path := filepath.Join(cwd, "custom.json")
	writeSource(t, path, "explicit")

	if source := loadSource(t, path); source != "explicit" {
		t.Errorf("Load: got configuration from %q, want %q", source, "explicit")
	}
}

func TestLoadExplicitPathMustExist(t *testing.T) {
	cwd, _, _ := setup(t)
	writeSource(t, filepath.Join(cwd, "arbot.config.json"), "cwd")
	_, err := Load("missing.json")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load: got %v, want %v", err, fs.ErrNotExist)
	}
}

func TestLoadSearchOrder(t *testing.T) {
	cwd, _, xdg := setup(t)
	writeSource(t, filepath.Join(xdg, "arbot", "config.json"), "xdg")
	if source := loadSource(t, ""); source != "xdg" {
		t.Errorf("Load: got configuration from %q, want %q", source, "xdg")
	}

	// A file in the working directory takes precedence.
	writeSource(t, filepath.Join(cwd, "arbot.config.json"), "cwd")
	if source := loadSource(t, ""); source != "cwd" {
		t.Errorf("Load: got configuration from %q, want %q", source, "cwd")
	}
}

func TestLoadXDGDefaultsToHomeConfig(t *testing.T) {
	_, home, _ := setup(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	writeSource(t, filepath.Join(home, ".config", "arbot", "config.json"), "home")
	if source := loadSource(t, ""); source != "home" {
		t.Errorf("Load: got configuration from %q, want %q", source, "home")
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	for name, contents := range map[string]string{
		"syntax":        `{`,
		"unknown field": `{"unknown": true}`,
		"trailing data": `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			cwd, _, _ := setup(t)
			path := filepath.Join(cwd, "arbot.config.json")
			write(t, path, contents)
			if _, err := Load(path); err == nil {
				t.Fatal("Load: expected an error")
			}
		})
	}
}

func TestLoadSettings(t *testing.T) {
	cwd, _, _ := setup(t)
	path := filepath.Join(cwd, "arbot.config.json")
	write(t, path, `{
		"rpcs": {"1": "https://mainnet.example", "100": "https://gnosis.example"},
		"ipfs": "https://ipfs.example",
		"txServices": {"1": "https://tx-service.example/eth"},
		"sourcify": "https://sourcify.example/server",
		"consensus": "0x00000000000000000000000000000000000000aa",
		"oracle": "0x00000000000000000000000000000000000000bb"
	}`)

	config, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		RPCs:       map[uint64]string{1: "https://mainnet.example", 100: "https://gnosis.example"},
		IPFS:       "https://ipfs.example",
		TxServices: map[uint64]string{1: "https://tx-service.example/eth"},
		Sourcify:   "https://sourcify.example/server",
		Consensus:  ethrpc.Address{19: 0xaa},
		Oracle:     ethrpc.Address{19: 0xbb},
	}
	if !maps.Equal(config.RPCs, want.RPCs) || !maps.Equal(config.TxServices, want.TxServices) || config.IPFS != want.IPFS || config.Sourcify != want.Sourcify || config.Oracle != want.Oracle || config.Consensus != want.Consensus {
		t.Errorf("Load: got %+v, want %+v", config, want)
	}
}

func TestLoadRejectsInvalidOracle(t *testing.T) {
	cwd, _, _ := setup(t)
	path := filepath.Join(cwd, "arbot.config.json")
	write(t, path, `{"oracle": "0xaa"}`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load: expected an error")
	}
}

func TestLoadRejectsInvalidChainID(t *testing.T) {
	cwd, _, _ := setup(t)
	path := filepath.Join(cwd, "arbot.config.json")
	write(t, path, `{"rpcs": {"mainnet": "https://mainnet.example"}}`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load: expected an error")
	}
}
