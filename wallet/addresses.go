package wallet

import "go.sia.tech/core/types"

// AddressBalance returns the balance of a single address.
func (m *Manager) AddressBalance(address types.Address) (balance Balance, err error) {
	return m.store.AddressBalance(address)
}

// AddressEvents returns the events of a single address.
func (m *Manager) AddressEvents(address types.Address, offset, limit int) (events []Event, err error) {
	return m.store.AddressEvents(address, offset, limit)
}

// AddressSiacoinOutputs returns the unspent siacoin outputs for an address.
func (m *Manager) AddressSiacoinOutputs(address types.Address, offset, limit int) (siacoins []types.SiacoinElement, err error) {
	return m.store.AddressSiacoinOutputs(address, offset, limit)
}

// AddressSiafundOutputs returns the unspent siafund outputs for an address.
func (m *Manager) AddressSiafundOutputs(address types.Address, offset, limit int) (siafunds []types.SiafundElement, err error) {
	return m.store.AddressSiafundOutputs(address, offset, limit)
}
