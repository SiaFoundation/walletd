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
func (m *Manager) AddressSiacoinOutputs(address types.Address, offset, limit int) (siacoins []types.SiacoinElement, err error) {
	return m.store.AddressSiacoinOutputs(address, offset, limit)
}

// AddressSiafundOutputs returns the unspent siafund outputs for an address.
func (m *Manager) AddressSiafundOutputs(address types.Address, offset, limit int) (siafunds []types.SiafundElement, err error) {
	return m.store.AddressSiafundOutputs(address, offset, limit)
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
