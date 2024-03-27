package wallet

import (
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
)

type (
	// AddressBalance pairs an address with its balance.
	AddressBalance struct {
		Address types.Address `json:"address"`
		Balance
	}

	// An UpdateTx atomically updates the state of a store.
	UpdateTx interface {
		SiacoinStateElements() ([]types.StateElement, error)
		UpdateSiacoinStateElements([]types.StateElement) error

		SiafundStateElements() ([]types.StateElement, error)
		UpdateSiafundStateElements([]types.StateElement) error

		AddSiacoinElements([]types.SiacoinElement, types.ChainIndex) error
		RemoveSiacoinElements([]types.SiacoinElement, types.ChainIndex) error

		AddSiafundElements([]types.SiafundElement, types.ChainIndex) error
		RemoveSiafundElements([]types.SiafundElement, types.ChainIndex) error

		AddressRelevant(types.Address) (bool, error)

		ApplyMatureSiacoinBalance(types.ChainIndex) error
		AddEvents([]Event) error

		RevertMatureSiacoinBalance(types.ChainIndex) error
		RevertEvents(index types.ChainIndex) error
	}
)

// applyChainUpdate atomically applies a chain update to a store
func applyChainUpdate(tx UpdateTx, cau chain.ApplyUpdate) error {
	// update the immature balance of each relevant address
	if err := tx.ApplyMatureSiacoinBalance(cau.State.Index); err != nil {
		return fmt.Errorf("failed to get matured siacoin elements: %w", err)
	}

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
	var newSiacoinElements, spentSiacoinElements []types.SiacoinElement
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
			spentSiacoinElements = append(spentSiacoinElements, se)
		} else {
			newSiacoinElements = append(newSiacoinElements, se)
		}
	})

	if err := tx.AddSiacoinElements(newSiacoinElements, cau.State.Index); err != nil {
		return fmt.Errorf("failed to add siacoin elements: %w", err)
	} else if err := tx.RemoveSiacoinElements(spentSiacoinElements, cau.State.Index); err != nil {
		return fmt.Errorf("failed to remove siacoin elements: %w", err)
	}

	var newSiafundElements, spentSiafundElements []types.SiafundElement
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
			spentSiafundElements = append(spentSiafundElements, se)
		} else {
			newSiafundElements = append(newSiafundElements, se)
		}
	})

	if err := tx.AddSiafundElements(newSiafundElements, cau.State.Index); err != nil {
		return fmt.Errorf("failed to add siafund elements: %w", err)
	} else if err := tx.RemoveSiafundElements(spentSiafundElements, cau.State.Index); err != nil {
		return fmt.Errorf("failed to remove siafund elements: %w", err)
	}

	// add events
	relevant := func(addr types.Address) bool {
		relevant, err := tx.AddressRelevant(addr)
		if err != nil {
			panic(fmt.Errorf("failed to check if address is relevant: %w", err))
		}
		return relevant
	}
	if err := tx.AddEvents(AppliedEvents(cau.State, cau.Block, cau, relevant)); err != nil {
		return fmt.Errorf("failed to add events: %w", err)
	}

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
	return nil
}

// revertChainUpdate atomically reverts a chain update from a store
func revertChainUpdate(tx UpdateTx, cru chain.RevertUpdate, revertedIndex types.ChainIndex) error {
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

	var removedSiacoinElements, addedSiacoinElements []types.SiacoinElement
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
			addedSiacoinElements = append(addedSiacoinElements, se)
		} else {
			// delete any created siacoin elements
			removedSiacoinElements = append(removedSiacoinElements, se)
		}
	})

	if err := tx.AddSiacoinElements(addedSiacoinElements, revertedIndex); err != nil {
		return fmt.Errorf("failed to add siacoin elements: %w", err)
	} else if err := tx.RemoveSiacoinElements(removedSiacoinElements, revertedIndex); err != nil {
		return fmt.Errorf("failed to remove siacoin elements: %w", err)
	}

	var removedSiafundElements, addedSiafundElements []types.SiafundElement
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
			addedSiafundElements = append(addedSiafundElements, se)
		} else {
			// delete any created siafund elements
			removedSiafundElements = append(removedSiafundElements, se)
		}
	})

	// revert siafund element changes
	if err := tx.AddSiafundElements(addedSiafundElements, revertedIndex); err != nil {
		return fmt.Errorf("failed to add siafund elements: %w", err)
	} else if err := tx.RemoveSiafundElements(removedSiafundElements, revertedIndex); err != nil {
		return fmt.Errorf("failed to remove siafund elements: %w", err)
	}

	// revert mature siacoin balance for each relevant address
	if err := tx.RevertMatureSiacoinBalance(revertedIndex); err != nil {
		return fmt.Errorf("failed to get matured siacoin elements: %w", err)
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

	// revert events
	return tx.RevertEvents(revertedIndex)
}

func UpdateChainState(tx UpdateTx, reverted []chain.RevertUpdate, applied []chain.ApplyUpdate) error {
	for _, cru := range reverted {
		revertedIndex := types.ChainIndex{
			ID:     cru.Block.ID(),
			Height: cru.State.Index.Height + 1,
		}
		if err := revertChainUpdate(tx, cru, revertedIndex); err != nil {
			return fmt.Errorf("failed to revert chain update %q: %w", revertedIndex, err)
		}
	}

	for _, cau := range applied {
		if err := applyChainUpdate(tx, cau); err != nil {
			return fmt.Errorf("failed to apply chain update %q: %w", cau.State.Index, err)
		}
	}
	return nil
}
