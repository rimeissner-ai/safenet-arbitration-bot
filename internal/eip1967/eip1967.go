// Package eip1967 reads the addresses that a proxy stores in the storage slots
// of EIP-1967: its implementation, its admin, and its beacon.
package eip1967

import (
	"context"
	"fmt"

	"github.com/safe-research/safenet-arbitration-bot/internal/ethrpc"
	"github.com/safe-research/safenet-arbitration-bot/internal/keccak256"
)

// The slots of EIP-1967, each keccak256 of its name minus 1.
var (
	ImplementationSlot = slot("eip1967.proxy.implementation")
	AdminSlot          = slot("eip1967.proxy.admin")
	BeaconSlot         = slot("eip1967.proxy.beacon")
)

// Slot is a slot that an account stores, with the address in its low 20 bytes,
// which is the zero address if the slot is empty.
type Slot struct {
	Slot    ethrpc.Hash    `json:"slot"`
	Value   ethrpc.Hash    `json:"value"`
	Address ethrpc.Address `json:"address"`
	// Clean is whether the high 12 bytes of the value are zero, as they are for an
	// address that Solidity stores.
	Clean bool `json:"clean"`
}

// Slots are the EIP-1967 slots of an account.
type Slots struct {
	Implementation Slot `json:"implementation"`
	Admin          Slot `json:"admin"`
	Beacon         Slot `json:"beacon"`
}

// Read reads the EIP-1967 slots of the account at address, at the block.
func Read(ctx context.Context, eth *ethrpc.Client, address ethrpc.Address, block ethrpc.BlockNumber) (*Slots, error) {
	var slots Slots
	for _, s := range []struct {
		slot ethrpc.Hash
		into *Slot
	}{{ImplementationSlot, &slots.Implementation}, {AdminSlot, &slots.Admin}, {BeaconSlot, &slots.Beacon}} {
		value, err := eth.GetStorageAt(ctx, address, s.slot, block)
		if err != nil {
			return nil, fmt.Errorf("reading slot %s of %s: %w", s.slot, address, err)
		}
		*s.into = decode(s.slot, value)
	}
	return &slots, nil
}

// decode returns the slot with the address in its value.
func decode(slot, value ethrpc.Hash) Slot {
	s := Slot{Slot: slot, Value: value, Clean: true}
	copy(s.Address[:], value[12:])
	for _, b := range value[:12] {
		if b != 0 {
			s.Clean = false
		}
	}
	return s
}

// slot returns keccak256(name) - 1.
func slot(name string) ethrpc.Hash {
	h := ethrpc.Hash(keccak256.Hash([]byte(name)))
	for i := len(h) - 1; i >= 0; i-- {
		h[i]--
		if h[i] != 0xff {
			break
		}
	}
	return h
}
