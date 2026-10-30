// Package config loads the optional Arbot configuration file.
//
// Every setting has a sane default, so a missing configuration file is not an
// error: it is equivalent to an empty one.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/xdg"
)

// Config is the Arbot configuration.
type Config struct {
	// RPCs maps chain IDs to the RPC URLs to use for them, such as
	// {"1": "https://ethereum-rpc.publicnode.com"}. Chains without an entry use a
	// public RPC from Chainlist.
	RPCs map[uint64]string `json:"rpcs"`
	// IPFS is the base URL of the IPFS HTTP gateway to use, such as
	// "https://ipfs.filebase.io". If empty, a public gateway is used.
	IPFS string `json:"ipfs"`
	// TxServices maps chain IDs to the base URLs of the Safe Transaction Service to
	// use for them, such as {"1": "https://api.safe.global/tx-service/eth"}. Chains
	// without an entry use the public service for the chain, if it has one. An
	// empty URL leaves the service out.
	TxServices map[uint64]string `json:"txServices"`
	// Sourcify is the base URL of the Sourcify server to fetch verified contracts
	// from, such as "https://sourcify.dev/server". If empty, Sourcify's public
	// server is used.
	Sourcify string `json:"sourcify"`
	// Consensus is the address of the Consensus contract on Gnosis Chain that the
	// oracle's transaction proposals come from. If zero, the default deployment is
	// used.
	Consensus ethrpc.Address `json:"consensus"`
	// Oracle is the address of the SentinelOracle contract on Gnosis Chain to read
	// Safenet requests from. If zero, the default deployment is used.
	Oracle ethrpc.Address `json:"oracle"`
}

// Load reads the configuration file at path. If path is empty, it uses the
// first file that exists out of:
//
//  1. ./arbot.config.json
//  2. $XDG_CONFIG_HOME/arbot/config.json (defaulting to ~/.config)
//
// If no file exists, it returns the default configuration.
func Load(path string) (Config, error) {
	if path != "" {
		return load(path)
	}

	for _, path := range searchPaths() {
		config, err := load(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		return config, err
	}
	return Config{}, nil
}

// searchPaths returns the paths that Load checks when no path is given, in
// order of precedence.
func searchPaths() []string {
	paths := []string{"arbot.config.json"}
	if dir := xdg.ConfigHome(); dir != "" {
		paths = append(paths, filepath.Join(dir, "arbot", "config.json"))
	}
	return paths
}

func load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var config Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("invalid configuration file %s: %w", path, err)
	}
	if decoder.More() {
		return Config{}, fmt.Errorf("invalid configuration file %s: unexpected data after configuration object", path)
	}
	return config, nil
}
