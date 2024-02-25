package wallet

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"sync"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/wallet"
	"lukechampine.com/frand"
)

// A Seed is securely-generated entropy, used to derive an arbitrary number of
// keypairs.
type Seed struct {
	entropy *[32]byte
}

// PublicKey derives the public key for the specified index.
func (s Seed) PublicKey(index uint64) (pk types.PublicKey) {
	key := wallet.KeyFromSeed(s.entropy, index)
	copy(pk[:], key[len(key)-ed25519.PublicKeySize:])
	return
}

// PrivateKey derives the private key for the specified index.
func (s Seed) PrivateKey(index uint64) types.PrivateKey {
	key := wallet.KeyFromSeed(s.entropy, index)
	return key[:]
}

// NewSeed returns a random Seed.
func NewSeed() Seed {
	var entropy [32]byte
	frand.Read(entropy[:])
	return NewSeedFromEntropy(&entropy)
}

// NewSeedFromEntropy returns a the specified seed.
func NewSeedFromEntropy(entropy *[32]byte) Seed {
	return Seed{entropy}
}

// A SeedAddressVault generates and stores addresses from a seed.
type SeedAddressVault struct {
	seed      Seed
	lookahead uint64
	addrs     map[types.Address]uint64
	mu        sync.Mutex
}

func (sav *SeedAddressVault) gen(index uint64) {
	for index > uint64(len(sav.addrs)) {
		sav.addrs[types.StandardAddress(sav.seed.PublicKey(uint64(len(sav.addrs))))] = uint64(len(sav.addrs))
	}
}

// OwnsAddress returns true if addr was derived from the seed.
func (sav *SeedAddressVault) OwnsAddress(addr types.Address) bool {
	sav.mu.Lock()
	defer sav.mu.Unlock()
	index, ok := sav.addrs[addr]
	if ok {
		sav.gen(index + sav.lookahead)
	}
	return ok
}

// NewAddress returns a new address derived from the seed, along with
// descriptive metadata.
func (sav *SeedAddressVault) NewAddress(desc string) Address {
	sav.mu.Lock()
	defer sav.mu.Unlock()
	index := uint64(len(sav.addrs)) - sav.lookahead + 1
	sav.gen(index + sav.lookahead)
	policy := types.PolicyPublicKey(sav.seed.PublicKey(index))
	addr := policy.Address()
	return Address{
		Address:     addr,
		Description: desc,
		SpendPolicy: &policy,
		Metadata:    json.RawMessage(fmt.Sprintf(`{"keyIndex":%d}`, index)),
	}
}

// SignTransaction signs the specified transaction using keys derived from the
// wallet seed. If toSign is nil, SignTransaction will automatically add
// Signatures for each input owned by the seed. If toSign is not nil, it a list
// of IDs of Signatures already present in txn; SignTransaction will fill in the
// Signature field of each.
func (sav *SeedAddressVault) SignTransaction(cs consensus.State, txn *types.Transaction, toSign []types.Hash256) error {
	sav.mu.Lock()
	defer sav.mu.Unlock()

	if len(toSign) == 0 {
		// lazy mode: add standard sigs for every input we own
		for _, sci := range txn.SiacoinInputs {
			if index, ok := sav.addrs[sci.UnlockConditions.UnlockHash()]; ok {
				txn.Signatures = append(txn.Signatures, StandardTransactionSignature(types.Hash256(sci.ParentID)))
				SignTransaction(cs, txn, len(txn.Signatures)-1, sav.seed.PrivateKey(index))
			}
		}
		for _, sfi := range txn.SiafundInputs {
			if index, ok := sav.addrs[sfi.UnlockConditions.UnlockHash()]; ok {
				txn.Signatures = append(txn.Signatures, StandardTransactionSignature(types.Hash256(sfi.ParentID)))
				SignTransaction(cs, txn, len(txn.Signatures)-1, sav.seed.PrivateKey(index))
			}
		}
		return nil
	}

	sigAddr := func(id types.Hash256) (types.Address, bool) {
		for _, sci := range txn.SiacoinInputs {
			if types.Hash256(sci.ParentID) == id {
				return sci.UnlockConditions.UnlockHash(), true
			}
		}
		for _, sfi := range txn.SiafundInputs {
			if types.Hash256(sfi.ParentID) == id {
				return sfi.UnlockConditions.UnlockHash(), true
			}
		}
		for _, fcr := range txn.FileContractRevisions {
			if types.Hash256(fcr.ParentID) == id {
				return fcr.UnlockConditions.UnlockHash(), true
			}
		}
		return types.Address{}, false
	}

outer:
	for _, parent := range toSign {
		for sigIndex, sig := range txn.Signatures {
			if sig.ParentID == parent {
				if addr, ok := sigAddr(parent); !ok {
					return fmt.Errorf("ID %v not present in transaction", parent)
				} else if index, ok := sav.addrs[addr]; !ok {
					return fmt.Errorf("missing key for ID %v", parent)
				} else {
					SignTransaction(cs, txn, sigIndex, sav.seed.PrivateKey(index))
					continue outer
				}
			}
		}
		return fmt.Errorf("signature %v not present in transaction", parent)
	}
	return nil
}

// NewSeedAddressVault initializes a SeedAddressVault.
func NewSeedAddressVault(seed Seed, initialAddrs, lookahead uint64) *SeedAddressVault {
	sav := &SeedAddressVault{
		seed:      seed,
		lookahead: lookahead,
		addrs:     make(map[types.Address]uint64),
	}
	sav.gen(initialAddrs + lookahead)
	return sav
}
