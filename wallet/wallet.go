package wallet

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
)

type (
	// Balance is a summary of a siacoin and siafund balance
	Balance struct {
		Siacoins         types.Currency `json:"siacoins"`
		ImmatureSiacoins types.Currency `json:"immatureSiacoins"`
		Siafunds         uint64         `json:"siafunds"`
	}

	// An ID is a unique identifier for a wallet.
	ID int64

	// A Wallet is a collection of addresses and metadata.
	Wallet struct {
		ID          ID              `json:"id"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		DateCreated time.Time       `json:"dateCreated"`
		LastUpdated time.Time       `json:"lastUpdated"`
		Metadata    json.RawMessage `json:"metadata"`
	}

	// A Address is an address associated with a wallet.
	Address struct {
		Address     types.Address      `json:"address"`
		Description string             `json:"description"`
		SpendPolicy *types.SpendPolicy `json:"spendPolicy,omitempty"`
		Metadata    json.RawMessage    `json:"metadata"`
	}

	// A ChainUpdate is a set of changes to the consensus state.
	ChainUpdate interface {
		ForEachSiacoinElement(func(sce types.SiacoinElement, spent bool))
		ForEachSiafundElement(func(sfe types.SiafundElement, spent bool))
		ForEachFileContractElement(func(fce types.FileContractElement, rev *types.FileContractElement, resolved, valid bool))
		ForEachV2FileContractElement(func(fce types.V2FileContractElement, rev *types.V2FileContractElement, res types.V2FileContractResolutionType))
	}
)

// ErrNotFound is returned when a requested wallet or address is not found.
var ErrNotFound = errors.New("not found")

// UnmarshalText implements encoding.TextUnmarshaler.
func (w *ID) UnmarshalText(buf []byte) error {
	id, err := strconv.ParseInt(string(buf), 10, 64)
	if err != nil {
		return err
	}
	*w = ID(id)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (w ID) MarshalText() ([]byte, error) {
	return []byte(strconv.FormatInt(int64(w), 10)), nil
}

// StandardTransactionSignature is the most common form of TransactionSignature.
// It covers the entire transaction, references a sole public key, and has no
// timelock.
func StandardTransactionSignature(id types.Hash256) types.TransactionSignature {
	return types.TransactionSignature{
		ParentID:       id,
		CoveredFields:  types.CoveredFields{WholeTransaction: true},
		PublicKeyIndex: 0,
	}
}

// SignTransaction signs txn with the given key. The TransactionSignature object
// must already be present in txn at the given index.
func SignTransaction(cs consensus.State, txn *types.Transaction, sigIndex int, key types.PrivateKey) {
	tsig := &txn.Signatures[sigIndex]
	var sigHash types.Hash256
	if tsig.CoveredFields.WholeTransaction {
		sigHash = cs.WholeSigHash(*txn, tsig.ParentID, tsig.PublicKeyIndex, tsig.Timelock, tsig.CoveredFields.Signatures)
	} else {
		sigHash = cs.PartialSigHash(*txn, tsig.CoveredFields)
	}
	sig := key.SignHash(sigHash)
	tsig.Signature = sig[:]
}

// AppliedEvents extracts a list of relevant events from a chain update.
func AppliedEvents(cs consensus.State, b types.Block, cu ChainUpdate, relevant func(types.Address) bool) (events []Event) {
	addEvent := func(id types.Hash256, maturityHeight uint64, eventType string, v EventData, relevant []types.Address) {
		// dedup relevant addresses
		seen := make(map[types.Address]bool)
		unique := relevant[:0]
		for _, addr := range relevant {
			if !seen[addr] {
				unique = append(unique, addr)
				seen[addr] = true
			}
		}

		events = append(events, Event{
			ID:             id,
			Timestamp:      b.Timestamp,
			Index:          cs.Index,
			MaturityHeight: maturityHeight,
			Relevant:       unique,
			Type:           eventType,
			Data:           v,
		})
	}

	anythingRelevant := func() (ok bool) {
		cu.ForEachSiacoinElement(func(sce types.SiacoinElement, spent bool) {
			if ok || relevant(sce.SiacoinOutput.Address) {
				ok = true
			}
		})
		cu.ForEachSiafundElement(func(sfe types.SiafundElement, spent bool) {
			if ok || relevant(sfe.SiafundOutput.Address) {
				ok = true
			}
		})
		return
	}()
	if !anythingRelevant {
		return nil
	}

	// collect all elements
	sces := make(map[types.SiacoinOutputID]types.SiacoinElement)
	sfes := make(map[types.SiafundOutputID]types.SiafundElement)
	fces := make(map[types.FileContractID]types.FileContractElement)
	v2fces := make(map[types.FileContractID]types.V2FileContractElement)
	cu.ForEachSiacoinElement(func(sce types.SiacoinElement, spent bool) {
		sce.MerkleProof = nil
		sces[types.SiacoinOutputID(sce.ID)] = sce
	})
	cu.ForEachSiafundElement(func(sfe types.SiafundElement, spent bool) {
		sfe.MerkleProof = nil
		sfes[types.SiafundOutputID(sfe.ID)] = sfe
	})
	cu.ForEachFileContractElement(func(fce types.FileContractElement, rev *types.FileContractElement, resolved, valid bool) {
		fce.MerkleProof = nil
		fces[types.FileContractID(fce.ID)] = fce
	})
	cu.ForEachV2FileContractElement(func(fce types.V2FileContractElement, rev *types.V2FileContractElement, res types.V2FileContractResolutionType) {
		fce.MerkleProof = nil
		v2fces[types.FileContractID(fce.ID)] = fce
	})

	// handle v1 transactions
	for _, txn := range b.Transactions {
		addresses := make(map[types.Address]bool)
		e := &EventV1Transaction{
			Transaction:          txn,
			SpentSiacoinElements: make([]types.SiacoinElement, 0, len(txn.SiacoinInputs)),
			SpentSiafundElements: make([]types.SiafundElement, 0, len(txn.SiafundInputs)),
		}

		for _, sci := range txn.SiacoinInputs {
			sce, ok := sces[sci.ParentID]
			if !ok {
				continue
			}

			e.SpentSiacoinElements = append(e.SpentSiacoinElements, sce)
			if relevant(sce.SiacoinOutput.Address) {
				addresses[sce.SiacoinOutput.Address] = true
			}
		}
		for _, sco := range txn.SiacoinOutputs {
			if relevant(sco.Address) {
				addresses[sco.Address] = true
			}
		}

		for _, sfi := range txn.SiafundInputs {
			sfe, ok := sfes[sfi.ParentID]
			if !ok {
				continue
			}

			e.SpentSiafundElements = append(e.SpentSiafundElements, sfe)
			if relevant(sfe.SiafundOutput.Address) {
				addresses[sfe.SiafundOutput.Address] = true
			}

			outputID := sfi.ParentID.ClaimOutputID()
			if sfo, ok := sces[outputID]; ok && relevant(sfi.ClaimAddress) {
				addEvent(types.Hash256(outputID), cs.MaturityHeight(), EventTypeSiafundClaim, EventPayout{
					SiacoinElement: sfo,
				}, []types.Address{sfi.ClaimAddress})
			}
		}
		for _, sfo := range txn.SiafundOutputs {
			if relevant(sfo.Address) {
				addresses[sfo.Address] = true
			}
		}

		// skip transactions with no relevant addresses
		if len(addresses) == 0 {
			continue
		}

		relevant := make([]types.Address, 0, len(addresses))
		for addr := range addresses {
			relevant = append(relevant, addr)
		}

		addEvent(types.Hash256(txn.ID()), cs.Index.Height, EventTypeV1Transaction, e, relevant) // transaction maturity height is the current block height
	}

	// handle v2 transactions
	for _, txn := range b.V2Transactions() {
		addresses := make(map[types.Address]bool)
		for _, sci := range txn.SiacoinInputs {
			if !relevant(sci.Parent.SiacoinOutput.Address) {
				continue
			}
			addresses[sci.Parent.SiacoinOutput.Address] = true
		}
		for _, sco := range txn.SiacoinOutputs {
			if !relevant(sco.Address) {
				continue
			}
			addresses[sco.Address] = true
		}
		for _, sfi := range txn.SiafundInputs {
			if !relevant(sfi.Parent.SiafundOutput.Address) {
				continue
			}
			addresses[sfi.Parent.SiafundOutput.Address] = true

			outputID := types.SiafundOutputID(sfi.Parent.ID).ClaimOutputID()
			if sfo, ok := sces[outputID]; ok && relevant(sfi.ClaimAddress) {
				addEvent(types.Hash256(outputID), cs.MaturityHeight(), EventTypeSiafundClaim, EventPayout{
					SiacoinElement: sfo,
				}, []types.Address{sfi.ClaimAddress})
			}
		}
		for _, sco := range txn.SiafundOutputs {
			if !relevant(sco.Address) {
				continue
			}
			addresses[sco.Address] = true
		}

		// skip transactions with no relevant addresses
		if len(addresses) == 0 {
			continue
		}

		ev := EventV2Transaction(txn)
		relevant := make([]types.Address, 0, len(addresses))
		for addr := range addresses {
			relevant = append(relevant, addr)
		}
		addEvent(types.Hash256(txn.ID()), cs.Index.Height, EventTypeV2Transaction, ev, relevant) // transaction maturity height is the current block height
	}

	// handle missed contracts
	cu.ForEachFileContractElement(func(fce types.FileContractElement, rev *types.FileContractElement, resolved, valid bool) {
		if !resolved {
			return
		}

		if valid {
			for i := range fce.FileContract.ValidProofOutputs {
				address := fce.FileContract.ValidProofOutputs[i].Address
				if !relevant(address) {
					continue
				}

				outputID := types.FileContractID(fce.ID).ValidOutputID(i)
				addEvent(types.Hash256(outputID), cs.MaturityHeight(), EventTypeV1ContractResolution, EventV1ContractResolution{
					FileContract:   fce,
					SiacoinElement: sces[outputID],
					Missed:         false,
				}, []types.Address{address})
			}
		} else {
			for i := range fce.FileContract.MissedProofOutputs {
				address := fce.FileContract.MissedProofOutputs[i].Address
				if !relevant(address) {
					continue
				}

				outputID := types.FileContractID(fce.ID).MissedOutputID(i)
				addEvent(types.Hash256(outputID), cs.MaturityHeight(), EventTypeV1ContractResolution, EventV1ContractResolution{
					FileContract:   fce,
					SiacoinElement: sces[outputID],
					Missed:         true,
				}, []types.Address{address})
			}
		}
	})

	cu.ForEachV2FileContractElement(func(fce types.V2FileContractElement, rev *types.V2FileContractElement, res types.V2FileContractResolutionType) {
		if res == nil {
			return
		}

		var missed bool
		if _, ok := res.(*types.V2FileContractExpiration); ok {
			missed = true
		}

		if relevant(fce.V2FileContract.HostOutput.Address) {
			outputID := types.FileContractID(fce.ID).V2HostOutputID()
			addEvent(types.Hash256(outputID), cs.MaturityHeight(), EventTypeV2ContractResolution, EventV2ContractResolution{
				FileContract:   fce,
				Resolution:     res,
				SiacoinElement: sces[outputID],
				Missed:         missed,
			}, []types.Address{fce.V2FileContract.HostOutput.Address})
		}

		if relevant(fce.V2FileContract.RenterOutput.Address) {
			outputID := types.FileContractID(fce.ID).V2RenterOutputID()
			addEvent(types.Hash256(outputID), cs.MaturityHeight(), EventTypeV2ContractResolution, EventV2ContractResolution{
				FileContract:   fce,
				Resolution:     res,
				SiacoinElement: sces[outputID],
				Missed:         missed,
			}, []types.Address{fce.V2FileContract.RenterOutput.Address})
		}
	})

	// handle block rewards
	for i := range b.MinerPayouts {
		if relevant(b.MinerPayouts[i].Address) {
			outputID := cs.Index.ID.MinerOutputID(i)
			addEvent(types.Hash256(outputID), cs.MaturityHeight(), EventTypeMinerPayout, EventPayout{
				SiacoinElement: sces[outputID],
			}, []types.Address{b.MinerPayouts[i].Address})
		}
	}

	// handle foundation subsidy
	if relevant(cs.FoundationPrimaryAddress) {
		outputID := cs.Index.ID.FoundationOutputID()
		sce, ok := sces[outputID]
		if ok {
			addEvent(types.Hash256(outputID), cs.MaturityHeight(), EventTypeFoundationSubsidy, EventPayout{
				SiacoinElement: sce,
			}, []types.Address{sce.SiacoinOutput.Address})
		}
	}

	return events
}
