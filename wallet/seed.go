package wallet

import (
	"crypto/ed25519"

	"go.sia.tech/core/types"
	"go.sia.tech/core/wallet"
	"lukechampine.com/frand"
)

type Seed struct {
	entropy *[32]byte
}

// PublicKey derives the types.SiaPublicKey for the specified index.
func (s Seed) PublicKey(index uint64) (pk types.PublicKey) {
	key := wallet.KeyFromSeed(s.entropy, index)
	copy(pk[:], key[len(key)-ed25519.PublicKeySize:])
	return
}

// SecretKey derives the ed25519 private key for the specified index.
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

// A SeedAddressInfo contains the unlock conditions and key index for an
// address derived from a seed.
type SeedAddressInfo struct {
	UnlockConditions types.UnlockConditions `json:"unlockConditions"`
	KeyIndex         uint64                 `json:"keyIndex"`
}
