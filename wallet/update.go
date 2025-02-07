package wallet

import (
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.uber.org/zap"
)

type (
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

	// AppliedState contains all state changes made to a store after applying a chain
	// update.
	AppliedState struct {
		NumLeaves              uint64
		Events                 []Event
		CreatedSiacoinElements []types.SiacoinElement
		SpentSiacoinElements   []types.SiacoinElement
		CreatedSiafundElements []types.SiafundElement
		SpentSiafundElements   []types.SiafundElement
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

	// add new siacoin elements to the store
	for _, sced := range cau.SiacoinElementDiffs() {
		sce := sced.SiacoinElement
		if (sced.Created && sced.Spent) || sce.SiacoinOutput.Value.IsZero() {
			continue
		} else if relevant, err := tx.AddressRelevant(sce.SiacoinOutput.Address); err != nil {
			panic(err)
		} else if !relevant {
			continue
		}
		if sced.Spent {
			applied.SpentSiacoinElements = append(applied.SpentSiacoinElements, sce)
		} else {
			applied.CreatedSiacoinElements = append(applied.CreatedSiacoinElements, sce)
		}
	}
	for _, sfed := range cau.SiafundElementDiffs() {
		sfe := sfed.SiafundElement
		if (sfed.Created && sfed.Spent) || sfe.SiafundOutput.Value == 0 {
			continue
		} else if relevant, err := tx.AddressRelevant(sfe.SiafundOutput.Address); err != nil {
			panic(err)
		} else if !relevant {
			continue
		}
		if sfed.Spent {
			applied.SpentSiafundElements = append(applied.SpentSiafundElements, sfe)
		} else {
			applied.CreatedSiafundElements = append(applied.CreatedSiafundElements, sfe)
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

	// determine which siacoin and siafund elements are ephemeral
	//
	// note: I thought we could use LeafIndex == EphemeralLeafIndex, but
	// it seems to be set before the subscriber is called.
	created := make(map[types.Hash256]bool)
	ephemeral := make(map[types.Hash256]bool)
	for _, txn := range cru.Block.Transactions {
		for i := range txn.SiacoinOutputs {
			created[types.Hash256(txn.SiacoinOutputID(i))] = true
		}
		for _, input := range txn.SiacoinInputs {
			ephemeral[types.Hash256(input.ParentID)] = created[types.Hash256(input.ParentID)]
		}
		for i := range txn.SiafundOutputs {
			created[types.Hash256(txn.SiafundOutputID(i))] = true
		}
		for _, input := range txn.SiafundInputs {
			ephemeral[types.Hash256(input.ParentID)] = created[types.Hash256(input.ParentID)]
		}
	}

	for _, sced := range cru.SiacoinElementDiffs() {
		sce := sced.SiacoinElement
		if (sced.Created && sced.Spent) || sce.SiacoinOutput.Value.IsZero() {
			continue
		} else if relevant, err := tx.AddressRelevant(sce.SiacoinOutput.Address); err != nil {
			panic(err)
		} else if !relevant {
			continue
		}
		if sced.Spent {
			// re-add any spent siacoin elements
			reverted.UnspentSiacoinElements = append(reverted.UnspentSiacoinElements, sce)
		} else {
			// delete any created siacoin elements
			reverted.DeletedSiacoinElements = append(reverted.DeletedSiacoinElements, sce)
		}
	}
	for _, sfed := range cru.SiafundElementDiffs() {
		sfe := sfed.SiafundElement
		if (sfed.Created && sfed.Spent) || sfe.SiafundOutput.Value == 0 {
			continue
		} else if relevant, err := tx.AddressRelevant(sfe.SiafundOutput.Address); err != nil {
			panic(err)
		} else if !relevant {
			continue
		}
		if sfed.Spent {
			reverted.UnspentSiafundElements = append(reverted.UnspentSiafundElements, sfe)
		} else {
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
