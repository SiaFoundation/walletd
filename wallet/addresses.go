package wallet

import (
	"time"

	"go.sia.tech/core/types"
)

// CheckAddresses returns true if any of the addresses have been seen on the
// blockchain. This is a quick way to scan wallets for lookaheads.
func (m *Manager) CheckAddresses(address []types.Address) (bool, error) {
	return m.store.CheckAddresses(address)
}

// AddressBalance returns the balance of a single address.
func (m *Manager) AddressBalance(address types.Address) (balance Balance, err error) {
	return m.store.AddressBalance(address)
}

// AddressSiacoinOutputs returns the unspent siacoin outputs for an address.
func (m *Manager) AddressSiacoinOutputs(address types.Address, usePool bool, offset, limit int) ([]UnspentSiacoinElement, types.ChainIndex, error) {
	if !usePool {
		return m.store.AddressSiacoinOutputs(address, nil, offset, limit)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	spent := m.poolAddressSCSpent[address]
	var created []UnspentSiacoinElement
	for _, sce := range m.poolSCCreated {
		if sce.SiacoinOutput.Address != address {
			continue
		}

		sce.StateElement = sce.StateElement.Copy()
		created = append(created, UnspentSiacoinElement{
			SiacoinElement: sce,
		})
	}

	outputs, basis, err := m.store.AddressSiacoinOutputs(address, spent, offset, limit)
	if err != nil {
		return nil, types.ChainIndex{}, err
	} else if len(outputs) == limit {
		return outputs, basis, nil
	}
	return append(outputs, created...), basis, nil
}

// AddressSiafundOutputs returns the unspent siafund outputs for an address.
func (m *Manager) AddressSiafundOutputs(address types.Address, usePool bool, offset, limit int) ([]UnspentSiafundElement, types.ChainIndex, error) {
	if !usePool {
		return m.store.AddressSiafundOutputs(address, nil, offset, limit)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	spent := m.poolAddressSFSpent[address]
	var created []UnspentSiafundElement
	for _, sfe := range m.poolSFCreated {
		if sfe.SiafundOutput.Address != address {
			continue
		}
		sfe.StateElement = sfe.StateElement.Copy()
		created = append(created, UnspentSiafundElement{
			SiafundElement: sfe,
		})
	}

	outputs, basis, err := m.store.AddressSiafundOutputs(address, spent, offset, limit)
	if err != nil {
		return nil, types.ChainIndex{}, err
	} else if len(outputs) == limit {
		return outputs, basis, nil
	}
	return append(outputs, created...), basis, nil
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
