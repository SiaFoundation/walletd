package wallet

import (
	"time"

	"go.sia.tech/core/types"
)

// AddressBalance returns the balance of a single address.
func (m *Manager) AddressBalance(address types.Address) (balance Balance, err error) {
	return m.store.AddressBalance(address)
}

// AddressSiacoinOutputs returns the unspent siacoin outputs for an address.
func (m *Manager) AddressSiacoinOutputs(address types.Address, usePool bool, offset, limit int) ([]UnspentSiacoinElement, types.ChainIndex, error) {
	if !usePool {
		return m.store.AddressSiacoinOutputs(address, nil, offset, limit)
	}
	created := make(map[types.SiacoinOutputID]types.SiacoinElement)
	var spent []types.SiacoinOutputID
	for _, txn := range m.chain.PoolTransactions() {
		for _, sci := range txn.SiacoinInputs {
			if sci.UnlockConditions.UnlockHash() != address {
				continue
			}

			delete(created, sci.ParentID)
			spent = append(spent, sci.ParentID)
		}

		for i, sco := range txn.SiacoinOutputs {
			if sco.Address != address {
				continue
			}

			outputID := txn.SiacoinOutputID(i)
			sce := types.SiacoinElement{
				ID: outputID,
				StateElement: types.StateElement{
					LeafIndex: types.UnassignedLeafIndex,
				},
				SiacoinOutput: sco,
			}
			created[sce.ID] = sce
		}
	}
	for _, txn := range m.chain.V2PoolTransactions() {
		for _, sci := range txn.SiacoinInputs {
			if sci.Parent.SiacoinOutput.Address == address {
				spent = append(spent, sci.Parent.ID)
			}

			delete(created, sci.Parent.ID)
			spent = append(spent, sci.Parent.ID)
		}

		for i, sco := range txn.SiacoinOutputs {
			if sco.Address != address {
				continue
			}

			sce := txn.EphemeralSiacoinOutput(i)
			created[sce.ID] = sce
		}
	}

	outputs, basis, err := m.store.AddressSiacoinOutputs(address, spent, offset, limit)
	if err != nil {
		return nil, types.ChainIndex{}, err
	} else if len(outputs) == limit {
		return outputs, basis, nil
	}
	for _, sce := range created {
		outputs = append(outputs, UnspentSiacoinElement{
			SiacoinElement: sce,
		})
	}
	return outputs, basis, nil
}

// AddressSiafundOutputs returns the unspent siafund outputs for an address.
func (m *Manager) AddressSiafundOutputs(address types.Address, usePool bool, offset, limit int) (outputs []UnspentSiafundElement, basis types.ChainIndex, err error) {
	if !usePool {
		return m.store.AddressSiafundOutputs(address, nil, offset, limit)
	}

	var spent []types.SiafundOutputID
	for _, txn := range m.chain.PoolTransactions() {
		for _, input := range txn.SiafundInputs {
			if input.UnlockConditions.UnlockHash() == address {
				spent = append(spent, input.ParentID)
			}
		}
	}
	for _, txn := range m.chain.V2PoolTransactions() {
		for _, input := range txn.SiafundInputs {
			if input.Parent.SiafundOutput.Address == address {
				spent = append(spent, input.Parent.ID)
			}
		}
	}
	return m.store.AddressSiafundOutputs(address, spent, offset, limit)
}

// AddressEvents returns the events of a single address.
func (m *Manager) AddressEvents(address types.Address, offset, limit int) (events []Event, err error) {
	return m.store.AddressEvents(address, offset, limit)
}

// AddressUnconfirmedEvents returns the unconfirmed events for a single address.
func (m *Manager) AddressUnconfirmedEvents(address types.Address) ([]Event, error) {
	index := m.chain.Tip()
	index.Height++
	index.ID = types.BlockID{}
	timestamp := time.Now()

	v1, v2 := m.chain.PoolTransactions(), m.chain.V2PoolTransactions()

	relevantV1Txn := func(txn types.Transaction) bool {
		for _, output := range txn.SiacoinOutputs {
			if output.Address == address {
				return true
			}
		}
		for _, input := range txn.SiacoinInputs {
			if input.UnlockConditions.UnlockHash() == address {
				return true
			}
		}
		for _, output := range txn.SiafundOutputs {
			if output.Address == address {
				return true
			}
		}
		for _, input := range txn.SiafundInputs {
			if input.UnlockConditions.UnlockHash() == address {
				return true
			}
		}
		return false
	}

	relevantV1 := v1[:0]
	for _, txn := range v1 {
		if !relevantV1Txn(txn) {
			continue
		}
		relevantV1 = append(relevantV1, txn)
	}

	events, err := m.store.AnnotateV1Events(index, timestamp, relevantV1)
	if err != nil {
		return nil, err
	}

	for i := range events {
		events[i].Relevant = []types.Address{address}
	}

	relevantV2Txn := func(txn types.V2Transaction) bool {
		for _, output := range txn.SiacoinOutputs {
			if output.Address == address {
				return true
			}
		}
		for _, input := range txn.SiacoinInputs {
			if input.Parent.SiacoinOutput.Address == address {
				return true
			}
		}
		for _, output := range txn.SiafundOutputs {
			if output.Address == address {
				return true
			}
		}
		for _, input := range txn.SiafundInputs {
			if input.Parent.SiafundOutput.Address == address {
				return true
			}
		}
		return false
	}

	// Annotate v2 transactions.
	for _, txn := range v2 {
		if !relevantV2Txn(txn) {
			continue
		}

		events = append(events, Event{
			ID:             types.Hash256(txn.ID()),
			Index:          index,
			Timestamp:      timestamp,
			MaturityHeight: index.Height,
			Type:           EventTypeV2Transaction,
			Data:           EventV2Transaction(txn),
			Relevant:       []types.Address{address},
		})
	}
	return events, nil
}
