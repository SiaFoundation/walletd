package wallet

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	mnemonics "gitlab.com/NebulousLabs/entropy-mnemonics"
	"go.sia.tech/siad/types"
	"golang.org/x/crypto/blake2b"
	"lukechampine.com/frand"
)

type Seed struct {
	entropy *[32]byte
}

// deriveKey derives the keypair for the specified index. Note that s.siadSeed is
// used in the derivation, not s.entropy; this is what allows Seeds to be used
// with standard siad wallets. s.entropy is only used to provide a shorter seed
// phrase in the String method.
func (s Seed) deriveKeyPair(index uint64) (keypair [64]byte) {
	buf := make([]byte, len(s.entropy)+8)
	n := copy(buf, s.entropy[:])
	binary.LittleEndian.PutUint64(buf[n:], index)
	seed := blake2b.Sum256(buf)
	copy(keypair[:], ed25519.NewKeyFromSeed(seed[:]))
	return
}

// PublicKey derives the types.SiaPublicKey for the specified index.
func (s Seed) PublicKey(index uint64) types.SiaPublicKey {
	key := s.deriveKeyPair(index)
	return types.SiaPublicKey{
		Algorithm: types.SignatureEd25519,
		Key:       key[len(key)-ed25519.PublicKeySize:],
	}
}

// SecretKey derives the ed25519 private key for the specified index.
func (s Seed) SecretKey(index uint64) ed25519.PrivateKey {
	key := s.deriveKeyPair(index)
	return key[:]
}

// SeedFromEntropy returns the Seed derived from the supplied entropy.
func SeedFromEntropy(entropy [16]byte) Seed {
	sum := blake2b.Sum256(entropy[:])
	return Seed{&sum}
}

// SeedFromBIP39 returns the Seed derived from the supplied BIP39 phrase.
func SeedFromBIP39(str string) (Seed, error) {
	entropy, err := decodeBIP39Phrase(str)
	if err != nil {
		return Seed{}, err
	}
	return SeedFromEntropy(entropy), nil
}

// SeedFromPhrase returns the Seed derived from the supplied 28 word phrase.
func SeedFromPhrase(str string) (Seed, error) {
	// Ensure the string is all lowercase letters and spaces
	for _, char := range str {
		if unicode.IsUpper(char) {
			return Seed{}, errors.New("seed is not valid: all words must be lowercase")
		}
		if !unicode.IsLetter(char) && !unicode.IsSpace(char) {
			return Seed{}, fmt.Errorf("seed is not valid: illegal character '%v'", char)
		}
	}

	// Check seed has 28 or 29 words
	if len(strings.Fields(str)) != 28 && len(strings.Fields(str)) != 29 {
		return Seed{}, errors.New("seed is not valid: must be 28 or 29 words")
	}

	// Check for other formatting errors (English only)
	IsFormat := regexp.MustCompile(`^([a-z]{3,12}){1}( {1}[a-z]{3,12}){27,28}$`).MatchString
	if !IsFormat(str) {
		return Seed{}, errors.New("seed is not valid: invalid formatting")
	}

	// Decode the string into the checksummed byte slice.
	checksumSeedBytes, err := mnemonics.FromString(str, mnemonics.English)
	if err != nil {
		return Seed{}, err
	}

	// Ensure the seed is 38 bytes (this check is not too helpful since it doesn't
	// give any hints about what is wrong to the end user, which is why it's the
	// last thing checked)
	if len(checksumSeedBytes) != 38 {
		return Seed{}, fmt.Errorf("seed is not valid: illegal number of bytes '%v'", len(checksumSeedBytes))
	}

	// Copy the seed from the checksummed slice.
	seed := Seed{entropy: new([32]byte)}
	copy(seed.entropy[:], checksumSeedBytes[:32])
	checksum := blake2b.Sum256(seed.entropy[:])
	if !bytes.Equal(checksumSeedBytes[32:], checksum[:6]) {
		return Seed{}, errors.New("seed failed checksum verification")
	}
	return seed, nil
}

// NewSeed returns a random Seed.
func NewSeed() Seed {
	return SeedFromEntropy(frand.Entropy128())
}

// A SeedAddressInfo contains the unlock conditions and key index for an
// address derived from a seed.
type SeedAddressInfo struct {
	UnlockConditions types.UnlockConditions `json:"unlockConditions"`
	KeyIndex         uint64                 `json:"keyIndex"`
}
