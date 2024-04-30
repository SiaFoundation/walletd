package wallet

import (
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.uber.org/zap"
)

type (
	// AddressBalance pairs an address with its balance.
	AddressBalance struct {
		Address types.Address `json:"address"`
		Balance
	}

	AppliedState struct {
		Events                 []Event
		CreatedSiacoinElements []types.SiacoinElement
		SpentSiacoinElements   []types.SiacoinElement
		CreatedSiafundElements []types.SiafundElement
		SpentSiafundElements   []types.SiafundElement
	}

	RevertedState struct {
		UnspentSiacoinElements []types.SiacoinElement
		DeletedSiacoinElements []types.SiacoinElement
		UnspentSiafundElements []types.SiafundElement
		DeletedSiafundElements []types.SiafundElement
	}

	// An UpdateTx atomically updates the state of a store.
	UpdateTx interface {
		SiacoinStateElements() ([]types.StateElement, error)
		UpdateSiacoinStateElements([]types.StateElement) error

		SiafundStateElements() ([]types.StateElement, error)
		UpdateSiafundStateElements([]types.StateElement) error

		AddressRelevant(types.Address) (bool, error)

		ApplyIndex(types.ChainIndex, AppliedState) error
		RevertIndex(types.ChainIndex, RevertedState) error
	}
)

// applyChainUpdate atomically applies a chain update to a store
func applyChainUpdate(tx UpdateTx, cau chain.ApplyUpdate) error {
	var applied AppliedState

	// determine which siacoin and siafund elements are ephemeral
	//
	// note: I thought we could use LeafIndex == EphemeralLeafIndex, but
	// it seems to be set before the subscriber is called.
	created := make(map[types.Hash256]bool)
	ephemeral := make(map[types.Hash256]bool)
	for _, txn := range cau.Block.Transactions {
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

	// add new siacoin elements to the store
	cau.ForEachSiacoinElement(func(se types.SiacoinElement, spent bool) {
		if ephemeral[se.ID] {
			return
		}

		relevant, err := tx.AddressRelevant(se.SiacoinOutput.Address)
		if err != nil {
			panic(err)
		} else if !relevant {
			return
		}

		if spent {
			applied.SpentSiacoinElements = append(applied.SpentSiacoinElements, se)
		} else {
			applied.CreatedSiacoinElements = append(applied.CreatedSiacoinElements, se)
		}
	})

	cau.ForEachSiafundElement(func(se types.SiafundElement, spent bool) {
		if ephemeral[se.ID] {
			return
		}

		relevant, err := tx.AddressRelevant(se.SiafundOutput.Address)
		if err != nil {
			panic(err)
		} else if !relevant {
			return
		}

		if spent {
			applied.SpentSiafundElements = append(applied.SpentSiafundElements, se)
		} else {
			applied.CreatedSiafundElements = append(applied.CreatedSiafundElements, se)
		}
	})

	// add events
	relevant := func(addr types.Address) bool {
		relevant, err := tx.AddressRelevant(addr)
		if err != nil {
			panic(fmt.Errorf("failed to check if address is relevant: %w", err))
		}
		return relevant
	}
	applied.Events = AppliedEvents(cau.State, cau.Block, cau, relevant)

	// fetch all siacoin and siafund state elements
	siacoinStateElements, err := tx.SiacoinStateElements()
	if err != nil {
		return fmt.Errorf("failed to get siacoin state elements: %w", err)
	}

	// update siacoin element proofs
	for i := range siacoinStateElements {
		cau.UpdateElementProof(&siacoinStateElements[i])
	}

	if err := tx.UpdateSiacoinStateElements(siacoinStateElements); err != nil {
		return fmt.Errorf("failed to update siacoin state elements: %w", err)
	}

	siafundStateElements, err := tx.SiafundStateElements()
	if err != nil {
		return fmt.Errorf("failed to get siafund state elements: %w", err)
	}

	// update siafund element proofs
	for i := range siafundStateElements {
		cau.UpdateElementProof(&siafundStateElements[i])
	}

	if err := tx.UpdateSiafundStateElements(siafundStateElements); err != nil {
		return fmt.Errorf("failed to update siacoin state elements: %w", err)
	}

	if err := tx.ApplyIndex(cau.State.Index, applied); err != nil {
		return fmt.Errorf("failed to apply chain update %q: %w", cau.State.Index, err)
	}
	return nil
}

// revertChainUpdate atomically reverts a chain update from a store
func revertChainUpdate(tx UpdateTx, cru chain.RevertUpdate, revertedIndex types.ChainIndex) error {
	var reverted RevertedState

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

	cru.ForEachSiacoinElement(func(se types.SiacoinElement, spent bool) {
		if ephemeral[se.ID] {
			return
		}

		relevant, err := tx.AddressRelevant(se.SiacoinOutput.Address)
		if err != nil {
			panic(err)
		} else if !relevant {
			return
		}

		if spent {
			// re-add any spent siacoin elements
			reverted.UnspentSiacoinElements = append(reverted.UnspentSiacoinElements, se)
		} else {
			// delete any created siacoin elements
			reverted.DeletedSiacoinElements = append(reverted.DeletedSiacoinElements, se)
		}
	})

	cru.ForEachSiafundElement(func(se types.SiafundElement, spent bool) {
		if ephemeral[se.ID] {
			return
		}

		relevant, err := tx.AddressRelevant(se.SiafundOutput.Address)
		if err != nil {
			panic(err)
		} else if !relevant {
			return
		}

		if spent {
			// re-add any spent siafund elements
			reverted.UnspentSiafundElements = append(reverted.UnspentSiafundElements, se)
		} else {
			// delete any created siafund elements
			reverted.DeletedSiafundElements = append(reverted.DeletedSiafundElements, se)
		}
	})

	if err := tx.RevertIndex(revertedIndex, reverted); err != nil {
		return fmt.Errorf("failed to revert index %q: %w", revertedIndex, err)
	}

	siacoinElements, err := tx.SiacoinStateElements()
	if err != nil {
		return fmt.Errorf("failed to get siacoin state elements: %w", err)
	}
	for i := range siacoinElements {
		cru.UpdateElementProof(&siacoinElements[i])
	}
	if err := tx.UpdateSiacoinStateElements(siacoinElements); err != nil {
		return fmt.Errorf("failed to update siacoin state elements: %w", err)
	}

	// update siafund element proofs
	siafundElements, err := tx.SiafundStateElements()
	if err != nil {
		return fmt.Errorf("failed to get siafund state elements: %w", err)
	}
	for i := range siafundElements {
		cru.UpdateElementProof(&siafundElements[i])
	}
	if err := tx.UpdateSiafundStateElements(siafundElements); err != nil {
		return fmt.Errorf("failed to update siafund state elements: %w", err)
	}
	return nil
}

func UpdateChainState(tx UpdateTx, reverted []chain.RevertUpdate, applied []chain.ApplyUpdate, log *zap.Logger) error {
	for _, cru := range reverted {
		revertedIndex := types.ChainIndex{
			ID:     cru.Block.ID(),
			Height: cru.State.Index.Height + 1,
		}
		if err := revertChainUpdate(tx, cru, revertedIndex); err != nil {
			return fmt.Errorf("failed to revert chain update %q: %w", revertedIndex, err)
		}
		log.Debug("reverted chain update", zap.Stringer("blockID", revertedIndex.ID), zap.Uint64("height", revertedIndex.Height))
	}

	for _, cau := range applied {
		// apply the chain update
		if err := applyChainUpdate(tx, cau); err != nil {
			return fmt.Errorf("failed to apply chain update %q: %w", cau.State.Index, err)
		}
		log.Debug("applied chain update", zap.Stringer("blockID", cau.State.Index.ID), zap.Uint64("height", cau.State.Index.Height))
	}
	return nil
}
