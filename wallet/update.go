package wallet

import (
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.uber.org/zap"
)

const (
	// ElementSourceTransaction indicates that a siacoin element originated
	// from a transaction output.
	ElementSourceTransaction SiacoinElementSource = "transaction"
	// ElementSourceMiner indicates that a siacoin element originated from a
	// miner payout.
	ElementSourceMiner SiacoinElementSource = "minerPayout"
	// ElementSourceContract indicates that a siacoin element originated from a
	// file contract output.
	ElementSourceContract SiacoinElementSource = "contractPayout"
	// ElementSourceSiafund indicates that a siacoin element originated from a
	// siafund claim.
	ElementSourceSiafund SiacoinElementSource = "siafundClaim"
	// ElementSourceFoundationSubsidy indicates that a siacoin element originated
	// from a foundation subsidy payout.
	ElementSourceFoundationSubsidy SiacoinElementSource = "foundationSubsidy"
	// ElementSourceUnknown indicates that the source of a siacoin element is unknown.
	ElementSourceUnknown SiacoinElementSource = "unknown"
)

type (
	// SiacoinElementSource indicates the source of a siacoin element.
	SiacoinElementSource = string

	// A stateTreeUpdater is an interface for applying and reverting
	// Merkle tree updates.
	stateTreeUpdater interface {
		UpdateElementProof(*types.StateElement)
		ForEachTreeNode(fn func(row uint64, col uint64, h types.Hash256))
	}

	// A ProofUpdater is an interface for updating Merkle proofs.
	ProofUpdater interface {
		UpdateElementProof(*types.StateElement)
	}

	// AddressBalance pairs an address with its balance.
	AddressBalance struct {
		Address types.Address `json:"address"`
		Balance
	}

	// A SiacoinOrigin is analogous to txnid:vout in Bitcoin, indicating the
	// origin of a siacoin output.
	SiacoinOrigin struct {
		Source string        `json:"source"`
		ID     types.Hash256 `json:"id"`
		Index  uint64        `json:"index"`
	}

	// SpentSiacoinElement pairs a spent siacoin element with the ID of the
	// transaction that spent it.
	SpentSiacoinElement struct {
		types.SiacoinElement
		EventID types.TransactionID
	}

	// SpentSiafundElement pairs a spent siafund element with the ID of the
	// transaction that spent it.
	SpentSiafundElement struct {
		types.SiafundElement
		EventID types.TransactionID
	}

	// CreatedSiacoinElement pairs a created siacoin element with its source
	// and an origin ID.
	CreatedSiacoinElement struct {
		types.SiacoinElement
		Origin SiacoinOrigin
	}

	// AppliedState contains all state changes made to a store after applying a chain
	// update.
	AppliedState struct {
		NumLeaves              uint64
		Events                 []Event
		CreatedSiacoinElements []CreatedSiacoinElement
		SpentSiacoinElements   []SpentSiacoinElement
		CreatedSiafundElements []types.SiafundElement
		SpentSiafundElements   []SpentSiafundElement
	}

	// RevertedState contains all state changes made to a store after reverting
	// a chain update.
	RevertedState struct {
		NumLeaves              uint64
		UnspentSiacoinElements []types.SiacoinElement
		DeletedSiacoinElements []types.SiacoinElement
		UnspentSiafundElements []types.SiafundElement
		DeletedSiafundElements []types.SiafundElement
	}

	// A TreeNodeUpdate contains the hash of a Merkle tree node and its row and
	// column indices.
	TreeNodeUpdate struct {
		Hash   types.Hash256
		Row    int
		Column int
	}

	// An UpdateTx atomically updates the state of a store.
	UpdateTx interface {
		UpdateStateElementProofs(ProofUpdater) error
		UpdateStateTree([]TreeNodeUpdate) error

		AddressRelevant(types.Address) (bool, error)

		ApplyIndex(types.ChainIndex, AppliedState) error
		RevertIndex(types.ChainIndex, RevertedState) error
	}
)

// updateStateElements updates the state elements in a store according to the
// changes made by a chain update.
func updateStateElements(tx UpdateTx, update stateTreeUpdater, indexMode IndexMode) error {
	if indexMode == IndexModeNone {
		panic("updateStateElements called with IndexModeNone") // developer error
	}

	if indexMode == IndexModeFull {
		var updates []TreeNodeUpdate
		update.ForEachTreeNode(func(row, col uint64, h types.Hash256) {
			updates = append(updates, TreeNodeUpdate{h, int(row), int(col)})
		})
		return tx.UpdateStateTree(updates)
	} else {
		return tx.UpdateStateElementProofs(update)
	}
}

// applyChainUpdate atomically applies a chain update to a store
func applyChainUpdate(tx UpdateTx, cau chain.ApplyUpdate, indexMode IndexMode) error {
	applied := AppliedState{
		NumLeaves: cau.State.Elements.NumLeaves,
	}

	scoOrigins := make(map[types.SiacoinOutputID]SiacoinOrigin)
	spentEventIDs := make(map[types.Hash256]types.TransactionID)
	for _, txn := range cau.Block.Transactions {
		txnID := txn.ID()
		for _, input := range txn.SiacoinInputs {
			spentEventIDs[types.Hash256(input.ParentID)] = txnID
		}
		for i, input := range txn.SiafundInputs {
			spentEventIDs[types.Hash256(input.ParentID)] = txnID
			scoOrigins[input.ParentID.ClaimOutputID()] = SiacoinOrigin{
				Source: ElementSourceSiafund,
				ID:     types.Hash256(txnID),
				Index:  uint64(i),
			}
		}
		// add sources for siacoin utxos
		for i := range txn.SiacoinOutputs {
			scoID := txn.SiacoinOutputID(i)
			scoOrigins[scoID] = SiacoinOrigin{
				Source: ElementSourceTransaction,
				ID:     types.Hash256(txnID),
				Index:  uint64(i),
			}
		}
	}
	for _, txn := range cau.Block.V2Transactions() {
		txnID := txn.ID()
		for _, input := range txn.SiacoinInputs {
			spentEventIDs[types.Hash256(input.Parent.ID)] = txnID
		}
		for i, input := range txn.SiafundInputs {
			spentEventIDs[types.Hash256(input.Parent.ID)] = txnID
			scoOrigins[input.Parent.ID.V2ClaimOutputID()] = SiacoinOrigin{
				Source: ElementSourceSiafund,
				ID:     types.Hash256(txnID),
				Index:  uint64(i),
			}
		}

		// add sources for siacoin utxos
		for i := range txn.SiacoinOutputs {
			scoID := txn.SiacoinOutputID(txnID, i)
			scoOrigins[scoID] = SiacoinOrigin{
				Source: ElementSourceTransaction,
				ID:     types.Hash256(txnID),
				Index:  uint64(i),
			}
		}
	}

	// determine sources for miner payout utxos
	blockID := cau.Block.ID()
	for i := range cau.Block.MinerPayouts {
		scoID := blockID.MinerOutputID(i)
		scoOrigins[scoID] = SiacoinOrigin{
			Source: ElementSourceMiner,
			ID:     types.Hash256(blockID),
			Index:  uint64(i),
		}
	}

	// source for possible foundation subsidy utxo
	scoOrigins[blockID.FoundationOutputID()] = SiacoinOrigin{
		Source: ElementSourceFoundationSubsidy,
		ID:     types.Hash256(blockID),
		Index:  0,
	}
	// determine sources for file contract utxos
	for _, diff := range cau.FileContractElementDiffs() {
		if !diff.Resolved {
			continue
		}

		fce := diff.FileContractElement
		if rev, ok := diff.RevisionElement(); ok {
			fce = rev
		}

		for i := range fce.FileContract.ValidProofOutputs {
			scoID := fce.ID.ValidOutputID(i)
			scoOrigins[scoID] = SiacoinOrigin{
				Source: ElementSourceContract,
				ID:     types.Hash256(fce.ID),
				Index:  uint64(i),
			}
		}
		for i := range fce.FileContract.MissedProofOutputs {
			scoID := fce.ID.MissedOutputID(i)
			scoOrigins[scoID] = SiacoinOrigin{
				Source: ElementSourceContract,
				ID:     types.Hash256(fce.ID),
				Index:  uint64(i),
			}
		}
	}

	// determine sources for V2 file contract utxos
	for _, diff := range cau.V2FileContractElementDiffs() {
		if diff.Resolution == nil {
			continue
		}

		scoOrigins[diff.V2FileContractElement.ID.V2HostOutputID()] = SiacoinOrigin{
			Source: ElementSourceContract,
			ID:     types.Hash256(diff.V2FileContractElement.ID),
			Index:  0,
		}
		scoOrigins[diff.V2FileContractElement.ID.V2RenterOutputID()] = SiacoinOrigin{
			Source: ElementSourceContract,
			ID:     types.Hash256(diff.V2FileContractElement.ID),
			Index:  1,
		}
	}

	// add new siafund elements to the store
	for _, sfed := range cau.SiafundElementDiffs() {
		sfe := sfed.SiafundElement
		if relevant, err := tx.AddressRelevant(sfe.SiafundOutput.Address); err != nil {
			panic(err)
		} else if !relevant {
			continue
		}

		// handle outputs that were created and spent in the same block
		if sfed.Created {
			applied.CreatedSiafundElements = append(applied.CreatedSiafundElements, sfe)
		}

		if sfed.Spent {
			spentTxnID, ok := spentEventIDs[types.Hash256(sfe.ID)]
			if !ok {
				panic(fmt.Errorf("missing transaction ID for spent siafund element %v", sfe.ID))
			}
			applied.SpentSiafundElements = append(applied.SpentSiafundElements, SpentSiafundElement{
				SiafundElement: sfe,
				EventID:        spentTxnID,
			})
		}
	}

	// add new siacoin elements to the store
	for _, sced := range cau.SiacoinElementDiffs() {
		sce := sced.SiacoinElement
		if relevant, err := tx.AddressRelevant(sce.SiacoinOutput.Address); err != nil {
			panic(err)
		} else if !relevant {
			continue
		}

		// handle outputs that were created and spent in the same block
		if sced.Created {
			origin, ok := scoOrigins[sce.ID]
			if !ok {
				panic("missing origin for created siacoin element " + sce.ID.String())
			}
			applied.CreatedSiacoinElements = append(applied.CreatedSiacoinElements, CreatedSiacoinElement{
				SiacoinElement: sce,
				Origin:         origin,
			})
		}

		if sced.Spent {
			spentTxnID, ok := spentEventIDs[types.Hash256(sce.ID)]
			if !ok {
				panic(fmt.Errorf("missing transaction ID for spent siacoin element %v", sce.ID))
			}
			applied.SpentSiacoinElements = append(applied.SpentSiacoinElements, SpentSiacoinElement{
				SiacoinElement: sce,
				EventID:        spentTxnID,
			})
		}
	}

	// add events
	relevant := func(addr types.Address) bool {
		relevant, err := tx.AddressRelevant(addr)
		if err != nil {
			panic(fmt.Errorf("failed to check if address is relevant: %w", err))
		}
		return relevant
	}
	applied.Events = AppliedEvents(cau.State, cau.Block, cau, relevant)

	if err := updateStateElements(tx, cau, indexMode); err != nil {
		return fmt.Errorf("failed to update state elements: %w", err)
	} else if err := tx.ApplyIndex(cau.State.Index, applied); err != nil {
		return fmt.Errorf("failed to apply index: %w", err)
	}
	return nil
}

// revertChainUpdate atomically reverts a chain update from a store
func revertChainUpdate(tx UpdateTx, cru chain.RevertUpdate, revertedIndex types.ChainIndex, indexMode IndexMode) error {
	reverted := RevertedState{
		NumLeaves: cru.State.Elements.NumLeaves,
	}

	for _, sced := range cru.SiacoinElementDiffs() {
		sce := sced.SiacoinElement
		if relevant, err := tx.AddressRelevant(sce.SiacoinOutput.Address); err != nil {
			panic(err)
		} else if !relevant {
			continue
		}

		if sced.Spent {
			// unspend any spent siacoin elements
			reverted.UnspentSiacoinElements = append(reverted.UnspentSiacoinElements, sce)
		}

		if sced.Created {
			// delete any created siacoin elements
			reverted.DeletedSiacoinElements = append(reverted.DeletedSiacoinElements, sce)
		}
	}
	for _, sfed := range cru.SiafundElementDiffs() {
		sfe := sfed.SiafundElement
		if relevant, err := tx.AddressRelevant(sfe.SiafundOutput.Address); err != nil {
			panic(err)
		} else if !relevant {
			continue
		}

		if sfed.Spent {
			// unspend any spent siafund elements
			reverted.UnspentSiafundElements = append(reverted.UnspentSiafundElements, sfe)
		}

		if sfed.Created {
			// delete any created siafund elements
			reverted.DeletedSiafundElements = append(reverted.DeletedSiafundElements, sfe)
		}
	}

	if err := tx.RevertIndex(revertedIndex, reverted); err != nil {
		return fmt.Errorf("failed to revert index: %w", err)
	}
	return updateStateElements(tx, cru, indexMode)
}

// UpdateChainState atomically updates the state of a store with a set of
// updates from the chain manager.
func UpdateChainState(tx UpdateTx, reverted []chain.RevertUpdate, applied []chain.ApplyUpdate, indexMode IndexMode, log *zap.Logger) error {
	for _, cru := range reverted {
		revertedIndex := types.ChainIndex{
			ID:     cru.Block.ID(),
			Height: cru.State.Index.Height + 1,
		}
		if err := revertChainUpdate(tx, cru, revertedIndex, indexMode); err != nil {
			return fmt.Errorf("failed to revert chain update %q: %w", revertedIndex, err)
		}
		log.Debug("reverted chain update", zap.Stringer("blockID", revertedIndex.ID), zap.Uint64("height", revertedIndex.Height))
	}

	for _, cau := range applied {
		// apply the chain update
		if err := applyChainUpdate(tx, cau, indexMode); err != nil {
			return fmt.Errorf("failed to apply chain update %q: %w", cau.State.Index, err)
		}
		log.Debug("applied chain update", zap.Stringer("blockID", cau.State.Index.ID), zap.Uint64("height", cau.State.Index.Height))
	}
	return nil
}
