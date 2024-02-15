package wallet

import (
	"fmt"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
)

type (
	AddressBalance struct {
		Address types.Address `json:"address"`
		Balance
	}

	ApplyTx interface {
		SiacoinStateElements() ([]types.StateElement, error)
		UpdateSiacoinStateElements([]types.StateElement) error

		SiafundStateElements() ([]types.StateElement, error)
		UpdateSiafundStateElements([]types.StateElement) error

		AddressRelevant(types.Address) (bool, error)
		AddressBalance(types.Address) (Balance, error)
		UpdateBalances([]AddressBalance) error

		MaturedSiacoinElements(types.ChainIndex) ([]types.SiacoinElement, error)
		AddSiacoinElements([]types.SiacoinElement) error
		RemoveSiacoinElements([]types.SiacoinOutputID) error

		AddSiafundElements([]types.SiafundElement) error
		RemoveSiafundElements([]types.SiafundOutputID) error

		AddEvents([]Event) error
	}

	RevertTx interface {
		RevertEvents(types.BlockID) error

		SiacoinStateElements() ([]types.StateElement, error)
		UpdateSiacoinStateElements([]types.StateElement) error

		SiafundStateElements() ([]types.StateElement, error)
		UpdateSiafundStateElements([]types.StateElement) error

		AddressRelevant(types.Address) (bool, error)
		AddressBalance(types.Address) (Balance, error)
		UpdateBalances([]AddressBalance) error

		MaturedSiacoinElements(types.ChainIndex) ([]types.SiacoinElement, error)
		AddSiacoinElements([]types.SiacoinElement) error
		RemoveSiacoinElements([]types.SiacoinOutputID) error
	}
)

// ApplyChainUpdates atomically applies a set of chain updates to a store
func ApplyChainUpdates(tx ApplyTx, updates []*chain.ApplyUpdate) error {
	var events []Event
	balances := make(map[types.Address]Balance)
	newSiacoinElements := make(map[types.SiacoinOutputID]types.SiacoinElement)
	newSiafundElements := make(map[types.SiafundOutputID]types.SiafundElement)
	spentSiacoinElements := make(map[types.SiacoinOutputID]bool)
	spentSiafundElements := make(map[types.SiafundOutputID]bool)

	updateBalance := func(addr types.Address, fn func(b *Balance)) error {
		balance, ok := balances[addr]
		if !ok {
			var err error
			balance, err = tx.AddressBalance(addr)
			if err != nil {
				return fmt.Errorf("failed to get address balance: %w", err)
			}
		}

		fn(&balance)
		balances[addr] = balance
		return nil
	}

	// fetch all siacoin and siafund state elements
	siacoinStateElements, err := tx.SiacoinStateElements()
	if err != nil {
		return fmt.Errorf("failed to get siacoin state elements: %w", err)
	}
	siafundStateElements, err := tx.SiafundStateElements()
	if err != nil {
		return fmt.Errorf("failed to get siafund state elements: %w", err)
	}

	for _, cau := range updates {
		// update the immature balance of each relevant address
		matured, err := tx.MaturedSiacoinElements(cau.State.Index)
		if err != nil {
			return fmt.Errorf("failed to get matured siacoin elements: %w", err)
		}
		for _, se := range matured {
			err := updateBalance(se.SiacoinOutput.Address, func(b *Balance) {
				b.Immature = b.Immature.Sub(se.SiacoinOutput.Value)
				b.Siacoin = b.Siacoin.Add(se.SiacoinOutput.Value)
			})
			if err != nil {
				return fmt.Errorf("failed to update address balance: %w", err)
			}
		}

		// add new siacoin elements to the store
		var siacoinElementErr error
		cau.ForEachSiacoinElement(func(se types.SiacoinElement, spent bool) {
			if siacoinElementErr != nil {
				return
			}

			if se.LeafIndex == types.EphemeralLeafIndex {
				return
			}

			relevant, err := tx.AddressRelevant(se.SiacoinOutput.Address)
			if err != nil {
				siacoinElementErr = fmt.Errorf("failed to check if address is relevant: %w", err)
				return
			} else if !relevant {
				return
			}

			if spent {
				delete(newSiacoinElements, types.SiacoinOutputID(se.ID))
				spentSiacoinElements[types.SiacoinOutputID(se.ID)] = true
			} else {
				newSiacoinElements[types.SiacoinOutputID(se.ID)] = se
			}

			err = updateBalance(se.SiacoinOutput.Address, func(b *Balance) {
				switch {
				case se.MaturityHeight > cau.State.Index.Height:
					b.Immature = b.Immature.Add(se.SiacoinOutput.Value)
				case spent:
					b.Siacoin = b.Siacoin.Sub(se.SiacoinOutput.Value)
				default:
					b.Siacoin = b.Siacoin.Add(se.SiacoinOutput.Value)
				}
			})
			if err != nil {
				siacoinElementErr = fmt.Errorf("failed to update address balance: %w", err)
				return
			}
		})
		if siacoinElementErr != nil {
			return fmt.Errorf("failed to add siacoin elements: %w", siacoinElementErr)
		}

		var siafundElementErr error
		cau.ForEachSiafundElement(func(se types.SiafundElement, spent bool) {
			if siafundElementErr != nil {
				return
			}

			relevant, err := tx.AddressRelevant(se.SiafundOutput.Address)
			if err != nil {
				siafundElementErr = fmt.Errorf("failed to check if address is relevant: %w", err)
				return
			} else if !relevant {
				return
			}

			if spent {
				delete(newSiafundElements, types.SiafundOutputID(se.ID))
				spentSiafundElements[types.SiafundOutputID(se.ID)] = true
			} else {
				newSiafundElements[types.SiafundOutputID(se.ID)] = se
			}

			err = updateBalance(se.SiafundOutput.Address, func(b *Balance) {
				if spent {
					b.Siafund -= se.SiafundOutput.Value
				} else {
					b.Siafund += se.SiafundOutput.Value
				}
			})
			if err != nil {
				siafundElementErr = fmt.Errorf("failed to update address balance: %w", err)
				return
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
		if err != nil {
			return fmt.Errorf("failed to get applied events: %w", err)
		}
		events = append(events, AppliedEvents(cau.State, cau.Block, cau, relevant)...)

		// update siacoin element proofs
		for id := range newSiacoinElements {
			ele := newSiacoinElements[id]
			cau.UpdateElementProof(&ele.StateElement)
			newSiacoinElements[id] = ele
		}
		for i := range siacoinStateElements {
			cau.UpdateElementProof(&siacoinStateElements[i])
		}

		// update siafund element proofs
		for id := range newSiafundElements {
			ele := newSiafundElements[id]
			cau.UpdateElementProof(&ele.StateElement)
			newSiafundElements[id] = ele
		}
		for i := range siafundStateElements {
			cau.UpdateElementProof(&siafundStateElements[i])
		}
	}

	// update the address balances
	balanceChanges := make([]AddressBalance, 0, len(balances))
	for addr, balance := range balances {
		balanceChanges = append(balanceChanges, AddressBalance{
			Address: addr,
			Balance: balance,
		})
	}
	if err = tx.UpdateBalances(balanceChanges); err != nil {
		return fmt.Errorf("failed to update address balance: %w", err)
	}

	// add the new siacoin elements
	siacoinElements := make([]types.SiacoinElement, 0, len(newSiacoinElements))
	for _, ele := range newSiacoinElements {
		siacoinElements = append(siacoinElements, ele)
	}
	if err = tx.AddSiacoinElements(siacoinElements); err != nil {
		return fmt.Errorf("failed to add siacoin elements: %w", err)
	}

	// remove the spent siacoin elements
	siacoinOutputIDs := make([]types.SiacoinOutputID, 0, len(spentSiacoinElements))
	for id := range spentSiacoinElements {
		siacoinOutputIDs = append(siacoinOutputIDs, id)
	}
	if err = tx.RemoveSiacoinElements(siacoinOutputIDs); err != nil {
		return fmt.Errorf("failed to remove siacoin elements: %w", err)
	}

	// add the new siafund elements
	siafundElements := make([]types.SiafundElement, 0, len(newSiafundElements))
	for _, ele := range newSiafundElements {
		siafundElements = append(siafundElements, ele)
	}
	if err = tx.AddSiafundElements(siafundElements); err != nil {
		return fmt.Errorf("failed to add siafund elements: %w", err)
	}

	// remove the spent siafund elements
	siafundOutputIDs := make([]types.SiafundOutputID, 0, len(spentSiafundElements))
	for id := range spentSiafundElements {
		siafundOutputIDs = append(siafundOutputIDs, id)
	}
	if err = tx.RemoveSiafundElements(siafundOutputIDs); err != nil {
		return fmt.Errorf("failed to remove siafund elements: %w", err)
	}

	// add new events
	if err = tx.AddEvents(events); err != nil {
		return fmt.Errorf("failed to add events: %w", err)
	}

	// update the siacoin state elements
	filteredStateElements := siacoinStateElements[:0]
	for _, se := range siacoinStateElements {
		if _, ok := spentSiacoinElements[types.SiacoinOutputID(se.ID)]; !ok {
			filteredStateElements = append(filteredStateElements, se)
		}
	}
	err = tx.UpdateSiacoinStateElements(filteredStateElements)
	if err != nil {
		return fmt.Errorf("failed to update siacoin state elements: %w", err)
	}

	// update the siafund state elements
	filteredStateElements = siafundStateElements[:0]
	for _, se := range siafundStateElements {
		if _, ok := spentSiafundElements[types.SiafundOutputID(se.ID)]; !ok {
			filteredStateElements = append(filteredStateElements, se)
		}
	}
	if err = tx.UpdateSiafundStateElements(filteredStateElements); err != nil {
		return fmt.Errorf("failed to update siafund state elements: %w", err)
	}

	return nil
}

// RevertChainUpdate atomically reverts a chain update from a store
func RevertChainUpdate(tx RevertTx, cru *chain.RevertUpdate) error {
	balances := make(map[types.Address]Balance)
	newSiacoinElements := make(map[types.SiacoinOutputID]types.SiacoinElement)
	newSiafundElements := make(map[types.SiafundOutputID]types.SiafundElement)
	spentSiacoinElements := make(map[types.SiacoinOutputID]bool)
	spentSiafundElements := make(map[types.SiafundOutputID]bool)

	updateBalance := func(addr types.Address, fn func(b *Balance)) error {
		balance, ok := balances[addr]
		if !ok {
			var err error
			balance, err = tx.AddressBalance(addr)
			if err != nil {
				return fmt.Errorf("failed to get address balance: %w", err)
			}
		}

		fn(&balance)
		balances[addr] = balance
		return nil
	}

	var siacoinElementErr error
	cru.ForEachSiacoinElement(func(se types.SiacoinElement, spent bool) {
		relevant, err := tx.AddressRelevant(se.SiacoinOutput.Address)
		if err != nil {
			siacoinElementErr = fmt.Errorf("failed to check if address is relevant: %w", err)
			return
		} else if !relevant {
			return
		}

		if !spent {
			newSiacoinElements[types.SiacoinOutputID(se.ID)] = se
		} else {
			spentSiacoinElements[types.SiacoinOutputID(se.ID)] = true
		}

		siacoinElementErr = updateBalance(se.SiacoinOutput.Address, func(b *Balance) {
			switch {
			case se.MaturityHeight > cru.State.Index.Height:
				b.Immature = b.Immature.Sub(se.SiacoinOutput.Value)
			case !spent:
				b.Siacoin = b.Siacoin.Add(se.SiacoinOutput.Value)
			default:
				b.Siacoin = b.Siacoin.Sub(se.SiacoinOutput.Value)
			}
		})
	})
	if siacoinElementErr != nil {
		return fmt.Errorf("failed to update address balance: %w", siacoinElementErr)
	}

	var siafundElementErr error
	cru.ForEachSiafundElement(func(se types.SiafundElement, spent bool) {
		relevant, err := tx.AddressRelevant(se.SiafundOutput.Address)
		if err != nil {
			siacoinElementErr = fmt.Errorf("failed to check if address is relevant: %w", err)
			return
		} else if !relevant {
			return
		}

		if !spent {
			newSiafundElements[types.SiafundOutputID(se.ID)] = se
		} else {
			spentSiafundElements[types.SiafundOutputID(se.ID)] = true
		}

		siafundElementErr = updateBalance(se.SiafundOutput.Address, func(b *Balance) {
			if spent {
				b.Siafund -= se.SiafundOutput.Value
			} else {
				b.Siafund += se.SiafundOutput.Value
			}
		})
	})
	if siafundElementErr != nil {
		return fmt.Errorf("failed to update address balance: %w", siafundElementErr)
	}

	balanceChanges := make([]AddressBalance, 0, len(balances))
	for addr, balance := range balances {
		balanceChanges = append(balanceChanges, AddressBalance{
			Address: addr,
			Balance: balance,
		})
	}
	if err := tx.UpdateBalances(balanceChanges); err != nil {
		return fmt.Errorf("failed to update address balance: %w", err)
	}

	return tx.RevertEvents(cru.Block.ID())
}
