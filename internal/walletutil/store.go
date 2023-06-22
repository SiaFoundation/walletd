package walletutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.sia.tech/core/chain"
	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
)

// An EphemeralStore stores wallet state in memory.
type EphemeralStore struct {
	tip    types.ChainIndex
	addrs  map[types.Address]json.RawMessage
	scos   map[types.SiacoinOutputID]types.SiacoinOutput
	sfos   map[types.SiafundOutputID]types.SiafundOutput
	events []wallet.Event
	mu     sync.Mutex
}

func (s *EphemeralStore) ownsAddress(addr types.Address) bool {
	_, ok := s.addrs[addr]
	return ok
}

// Events implements api.Wallet.
func (s *EphemeralStore) Events(since time.Time, max int) (events []wallet.Event, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if max == 0 {
			return
		} else if !event.Timestamp.After(since) {
			continue
		}
		events = append(events, event)
		max--
	}
	return
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
func (s *EphemeralStore) UnspentOutputs() (scos []wallet.SiacoinElement, sfos []wallet.SiafundElement, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sco := range s.scos {
		scos = append(scos, wallet.SiacoinElement{
			ID:            id,
			SiacoinOutput: sco,
		})
	}
	for id, sfo := range s.sfos {
		sfos = append(sfos, wallet.SiafundElement{
			ID:            id,
			SiafundOutput: sfo,
		})
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

// ProcessChainApplyUpdate implements chain.Subscriber.
func (s *EphemeralStore) ProcessChainApplyUpdate(cau *chain.ApplyUpdate, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	events := wallet.DiffEvents(cau.Block, cau.Diff, cau.State.Index, s.ownsAddress)
	s.events = append(s.events, events...)

	for _, tdiff := range cau.Diff.Transactions {
		for _, scod := range tdiff.SpentSiacoinOutputs {
			if s.ownsAddress(scod.Output.Address) {
				delete(s.scos, scod.ID)
			}
		}
		for _, scod := range tdiff.CreatedSiacoinOutputs {
			if s.ownsAddress(scod.Output.Address) {
				s.scos[scod.ID] = scod.Output
			}
		}
		for _, sfod := range tdiff.SpentSiafundOutputs {
			if s.ownsAddress(sfod.Output.Address) {
				delete(s.sfos, sfod.ID)
			}
		}
		for _, sfod := range tdiff.CreatedSiafundOutputs {
			if s.ownsAddress(sfod.Output.Address) {
				s.sfos[sfod.ID] = sfod.Output
			}
		}
	}
	for _, scod := range cau.Diff.MaturedSiacoinOutputs {
		if s.ownsAddress(scod.Output.Address) {
			s.scos[scod.ID] = scod.Output
		}
	}

	s.tip = cau.State.Index
	return nil
}

// ProcessChainRevertUpdate implements chain.Subscriber.
func (s *EphemeralStore) ProcessChainRevertUpdate(cru *chain.RevertUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// TODO: kinda wasteful
	events := wallet.DiffEvents(cru.Block, cru.Diff, cru.State.Index, s.ownsAddress)
	s.events = s.events[:len(s.events)-len(events)]

	for _, tdiff := range cru.Diff.Transactions {
		for _, scod := range tdiff.SpentSiacoinOutputs {
			if s.ownsAddress(scod.Output.Address) {
				s.scos[scod.ID] = scod.Output
			}
		}
		for _, scod := range tdiff.CreatedSiacoinOutputs {
			if s.ownsAddress(scod.Output.Address) {
				delete(s.scos, scod.ID)
			}
		}
		for _, sfod := range tdiff.SpentSiafundOutputs {
			if s.ownsAddress(sfod.Output.Address) {
				s.sfos[sfod.ID] = sfod.Output
			}
		}
		for _, sfod := range tdiff.CreatedSiafundOutputs {
			if s.ownsAddress(sfod.Output.Address) {
				delete(s.sfos, sfod.ID)
			}
		}
	}
	for _, scod := range cru.Diff.MaturedSiacoinOutputs {
		if s.ownsAddress(scod.Output.Address) {
			delete(s.scos, scod.ID)
		}
	}

	s.tip = cru.State.Index
	return nil
}

// NewEphemeralStore returns a new EphemeralStore.
func NewEphemeralStore() *EphemeralStore {
	return &EphemeralStore{
		addrs: make(map[types.Address]json.RawMessage),
		scos:  make(map[types.SiacoinOutputID]types.SiacoinOutput),
		sfos:  make(map[types.SiafundOutputID]types.SiafundOutput),
	}
}

// A JSONStore stores wallet state in memory, backed by a JSON file.
type JSONStore struct {
	*EphemeralStore
	path string
}

type persistData struct {
	Tip            types.ChainIndex
	Addresses      map[types.Address]json.RawMessage
	SiacoinOutputs map[types.SiacoinOutputID]types.SiacoinOutput
	SiafundOutputs map[types.SiafundOutputID]types.SiafundOutput
	Events         []wallet.Event
}

func (s *JSONStore) save() error {
	js, err := json.MarshalIndent(persistData{
		Tip:            s.tip,
		Addresses:      s.addrs,
		SiacoinOutputs: s.scos,
		SiafundOutputs: s.sfos,
		Events:         s.events,
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
	dst := filepath.Join(s.path, "wallet.json")
	f, err := os.Open(dst)
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
	s.scos = p.SiacoinOutputs
	s.sfos = p.SiafundOutputs
	s.events = p.Events
	return nil
}

// ProcessChainApplyUpdate implements chain.Subscriber.
func (s *JSONStore) ProcessChainApplyUpdate(cau *chain.ApplyUpdate, mayCommit bool) error {
	s.EphemeralStore.ProcessChainApplyUpdate(cau, mayCommit)
	if mayCommit {
		return s.save()
	}
	return nil
}

// ProcessChainRevertUpdate implements chain.Subscriber.
func (s *JSONStore) ProcessChainRevertUpdate(cru *chain.RevertUpdate) error {
	s.EphemeralStore.ProcessChainRevertUpdate(cru)
	return nil
}

// AddAddress implements api.Wallet.
func (s *JSONStore) AddAddress(addr types.Address, info json.RawMessage) error {
	if err := s.EphemeralStore.AddAddress(addr, info); err != nil {
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
