package walletutil

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"go.sia.tech/coreutils/chain"
	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
)

// An EphemeralStore stores wallet state in memory.
type EphemeralStore struct {
	tip    types.ChainIndex
	addrs  map[types.Address]json.RawMessage
	sces   map[types.SiacoinOutputID]types.SiacoinElement
	sfes   map[types.SiafundOutputID]types.SiafundElement
	events []wallet.Event
	mu     sync.Mutex
}

func (s *EphemeralStore) ownsAddress(addr types.Address) bool {
	_, ok := s.addrs[addr]
	return ok
}

// Events implements api.Wallet.
func (s *EphemeralStore) Events(offset, limit int) (events []wallet.Event, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit == -1 {
		limit = len(s.events)
	}
	if offset > len(s.events) {
		offset = len(s.events)
	}
	if offset+limit > len(s.events) {
		limit = len(s.events) - offset
	}
	// reverse
	es := make([]wallet.Event, limit)
	for i := range es {
		es[i] = s.events[len(s.events)-offset-i-1]
	}
	return es, nil
}

// Annotate implements api.Wallet.
func (s *EphemeralStore) Annotate(txns []types.Transaction) (ptxns []wallet.PoolTransaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, txn := range txns {
		ptxn := wallet.Annotate(txn, s.ownsAddress)
		if ptxn.Type != "unrelated" {
			ptxns = append(ptxns, ptxn)
		}
	}
	return
}

// UnspentOutputs implements api.Wallet.
func (s *EphemeralStore) UnspentOutputs() (sces []types.SiacoinElement, sfes []types.SiafundElement, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sco := range s.sces {
		sces = append(sces, sco)
	}
	for _, sfo := range s.sfes {
		sfes = append(sfes, sfo)
	}
	return
}

// Addresses implements api.Wallet.
func (s *EphemeralStore) Addresses() (map[types.Address]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	addrs := make(map[types.Address]json.RawMessage, len(s.addrs))
	for addr, info := range s.addrs {
		addrs[addr] = info
	}
	return addrs, nil
}

// AddAddress implements api.Wallet.
func (s *EphemeralStore) AddAddress(addr types.Address, info json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addrs[addr] = info
	return nil
}

// RemoveAddress implements api.Wallet.
func (s *EphemeralStore) RemoveAddress(addr types.Address) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.addrs[addr]; !ok {
		return nil
	}
	delete(s.addrs, addr)

	// filter outputs
	for scoid, sce := range s.sces {
		if sce.SiacoinOutput.Address == addr {
			delete(s.sces, scoid)
		}
	}
	for sfoid, sfe := range s.sfes {
		if sfe.SiafundOutput.Address == addr {
			delete(s.sfes, sfoid)
		}
	}

	// filter events
	relevantContract := func(fc types.FileContract) bool {
		for _, sco := range fc.ValidProofOutputs {
			if s.ownsAddress(sco.Address) {
				return true
			}
		}
		for _, sco := range fc.MissedProofOutputs {
			if s.ownsAddress(sco.Address) {
				return true
			}
		}
		return false
	}
	relevantV2Contract := func(fc types.V2FileContract) bool {
		return s.ownsAddress(fc.RenterOutput.Address) || s.ownsAddress(fc.HostOutput.Address)
	}
	relevantEvent := func(e wallet.Event) bool {
		switch e := e.Val.(type) {
		case *wallet.EventTransaction:
			for _, sce := range e.SiacoinInputs {
				if s.ownsAddress(sce.SiacoinOutput.Address) {
					return true
				}
			}
			for _, sce := range e.SiacoinOutputs {
				if s.ownsAddress(sce.SiacoinOutput.Address) {
					return true
				}
			}
			for _, sfe := range e.SiafundInputs {
				if s.ownsAddress(sfe.SiafundElement.SiafundOutput.Address) ||
					s.ownsAddress(sfe.ClaimElement.SiacoinOutput.Address) {
					return true
				}
			}
			for _, sfe := range e.SiafundOutputs {
				if s.ownsAddress(sfe.SiafundOutput.Address) {
					return true
				}
			}
			for _, fc := range e.FileContracts {
				if relevantContract(fc.FileContract.FileContract) || (fc.Revision != nil && relevantContract(*fc.Revision)) {
					return true
				}
			}
			for _, fc := range e.V2FileContracts {
				if relevantV2Contract(fc.FileContract.V2FileContract) || (fc.Revision != nil && relevantV2Contract(*fc.Revision)) {
					return true
				}
				if fc.Resolution != nil {
					switch r := fc.Resolution.(type) {
					case *types.V2FileContractFinalization:
						if relevantV2Contract(types.V2FileContract(*r)) {
							return true
						}
					case *types.V2FileContractRenewal:
						if relevantV2Contract(r.FinalRevision) || relevantV2Contract(r.InitialRevision) {
							return true
						}
					}
				}
			}
			return false
		case *wallet.EventMinerPayout:
			return s.ownsAddress(e.SiacoinOutput.SiacoinOutput.Address)
		case *wallet.EventMissedFileContract:
			for _, sce := range e.MissedOutputs {
				if s.ownsAddress(sce.SiacoinOutput.Address) {
					return true
				}
			}
			return false
		default:
			panic(fmt.Sprintf("unhandled event type %T", e))
		}
	}

	rem := s.events[:0]
	for _, e := range s.events {
		if relevantEvent(e) {
			rem = append(rem, e)
		}
	}
	s.events = rem
	return nil
}

// ProcessChainApplyUpdate implements chain.Subscriber.
func (s *EphemeralStore) ProcessChainApplyUpdate(cau *chain.ApplyUpdate, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	events := wallet.AppliedEvents(cau.State, cau.Block, cau, s.ownsAddress)
	s.events = append(s.events, events...)

	// add/remove outputs
	cau.ForEachSiacoinElement(func(sce types.SiacoinElement, spent bool) {
		if s.ownsAddress(sce.SiacoinOutput.Address) {
			if spent {
				delete(s.sces, types.SiacoinOutputID(sce.ID))
			} else {
				sce.MerkleProof = append([]types.Hash256(nil), sce.MerkleProof...)
				s.sces[types.SiacoinOutputID(sce.ID)] = sce
			}
		}
	})
	cau.ForEachSiafundElement(func(sfe types.SiafundElement, spent bool) {
		if s.ownsAddress(sfe.SiafundOutput.Address) {
			if spent {
				delete(s.sfes, types.SiafundOutputID(sfe.ID))
			} else {
				sfe.MerkleProof = append([]types.Hash256(nil), sfe.MerkleProof...)
				s.sfes[types.SiafundOutputID(sfe.ID)] = sfe
			}
		}
	})

	// update proofs
	for id, sce := range s.sces {
		cau.UpdateElementProof(&sce.StateElement)
		s.sces[id] = sce
	}
	for id, sfe := range s.sfes {
		cau.UpdateElementProof(&sfe.StateElement)
		s.sfes[id] = sfe
	}

	s.tip = cau.State.Index
	return nil
}

// ProcessChainRevertUpdate implements chain.Subscriber.
func (s *EphemeralStore) ProcessChainRevertUpdate(cru *chain.RevertUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// terribly inefficient, but not a big deal because reverts are infrequent
	numEvents := len(wallet.AppliedEvents(cru.State, cru.Block, cru, s.ownsAddress))
	s.events = s.events[:len(s.events)-numEvents]

	cru.ForEachSiacoinElement(func(sce types.SiacoinElement, spent bool) {
		if s.ownsAddress(sce.SiacoinOutput.Address) {
			if !spent {
				delete(s.sces, types.SiacoinOutputID(sce.ID))
			} else {
				sce.MerkleProof = append([]types.Hash256(nil), sce.MerkleProof...)
				s.sces[types.SiacoinOutputID(sce.ID)] = sce
			}
		}
	})
	cru.ForEachSiafundElement(func(sfe types.SiafundElement, spent bool) {
		if s.ownsAddress(sfe.SiafundOutput.Address) {
			if !spent {
				delete(s.sfes, types.SiafundOutputID(sfe.ID))
			} else {
				sfe.MerkleProof = append([]types.Hash256(nil), sfe.MerkleProof...)
				s.sfes[types.SiafundOutputID(sfe.ID)] = sfe
			}
		}
	})

	// update proofs
	for id, sce := range s.sces {
		cru.UpdateElementProof(&sce.StateElement)
		s.sces[id] = sce
	}
	for id, sfe := range s.sfes {
		cru.UpdateElementProof(&sfe.StateElement)
		s.sfes[id] = sfe
	}

	s.tip = cru.State.Index
	return nil
}

// NewEphemeralStore returns a new EphemeralStore.
func NewEphemeralStore() *EphemeralStore {
	return &EphemeralStore{
		addrs: make(map[types.Address]json.RawMessage),
		sces:  make(map[types.SiacoinOutputID]types.SiacoinElement),
		sfes:  make(map[types.SiafundOutputID]types.SiafundElement),
	}
}

// A JSONStore stores wallet state in memory, backed by a JSON file.
type JSONStore struct {
	*EphemeralStore
	path string
}

type persistData struct {
	Tip             types.ChainIndex
	Addresses       map[types.Address]json.RawMessage
	SiacoinElements map[types.SiacoinOutputID]types.SiacoinElement
	SiafundElements map[types.SiafundOutputID]types.SiafundElement
	Events          []wallet.Event
}

func (s *JSONStore) save() error {
	js, err := json.MarshalIndent(persistData{
		Tip:             s.tip,
		Addresses:       s.addrs,
		SiacoinElements: s.sces,
		SiafundElements: s.sfes,
		Events:          s.events,
	}, "", "  ")
	if err != nil {
		return err
	}

	f, err := os.OpenFile(s.path+"_tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0660)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(js); err != nil {
		return err
	} else if f.Sync(); err != nil {
		return err
	} else if f.Close(); err != nil {
		return err
	} else if err := os.Rename(s.path+"_tmp", s.path); err != nil {
		return err
	}
	return nil
}

func (s *JSONStore) load() error {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	defer f.Close()
	var p persistData
	if err := json.NewDecoder(f).Decode(&p); err != nil {
		return err
	}
	s.tip = p.Tip
	s.addrs = p.Addresses
	s.sces = p.SiacoinElements
	s.sfes = p.SiafundElements
	s.events = p.Events
	return nil
}

// ProcessChainApplyUpdate implements chain.Subscriber.
func (s *JSONStore) ProcessChainApplyUpdate(cau *chain.ApplyUpdate, mayCommit bool) error {
	err := s.EphemeralStore.ProcessChainApplyUpdate(cau, mayCommit)
	if err == nil && mayCommit {
		err = s.save()
	}
	return err
}

// ProcessChainRevertUpdate implements chain.Subscriber.
func (s *JSONStore) ProcessChainRevertUpdate(cru *chain.RevertUpdate) error {
	return s.EphemeralStore.ProcessChainRevertUpdate(cru)
}

// AddAddress implements api.Wallet.
func (s *JSONStore) AddAddress(addr types.Address, info json.RawMessage) error {
	if err := s.EphemeralStore.AddAddress(addr, info); err != nil {
		return err
	}
	return s.save()
}

// RemoveAddress implements api.Wallet.
func (s *JSONStore) RemoveAddress(addr types.Address) error {
	if err := s.EphemeralStore.RemoveAddress(addr); err != nil {
		return err
	}
	return s.save()
}

// NewJSONStore returns a new JSONStore.
func NewJSONStore(path string) (*JSONStore, types.ChainIndex, error) {
	s := &JSONStore{
		EphemeralStore: NewEphemeralStore(),
		path:           path,
	}
	err := s.load()
	return s, s.tip, err
}
