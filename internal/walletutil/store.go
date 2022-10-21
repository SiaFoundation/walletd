package walletutil

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
	"go.sia.tech/walletd/wallet"
)

type EphemeralStore struct {
	seedIndex uint64
	ccid      modules.ConsensusChangeID

	addrs     map[types.UnlockHash]wallet.SeedAddressInfo
	txns      map[types.TransactionID]wallet.Transaction
	outputsSC map[types.SiacoinOutputID]wallet.SiacoinElement
	outputsSF map[types.SiafundOutputID]wallet.SiafundElement
	contracts map[types.FileContractID]wallet.FileContractElement

	mu sync.Mutex
}

func relevantFileContract(fc types.FileContract, ownsAddress func(types.UnlockHash) bool) bool {
	relevant := false
	for _, sco := range fc.ValidProofOutputs {
		relevant = relevant || ownsAddress(sco.UnlockHash)
	}
	for _, sco := range fc.MissedProofOutputs {
		relevant = relevant || ownsAddress(sco.UnlockHash)
	}
	return relevant
}

func relevantTransaction(txn types.Transaction, ownsAddress func(types.UnlockHash) bool) bool {
	relevant := false
	for i := range txn.SiacoinInputs {
		relevant = relevant || ownsAddress(txn.SiacoinInputs[i].UnlockConditions.UnlockHash())
	}
	for i := range txn.SiacoinOutputs {
		relevant = relevant || ownsAddress(txn.SiacoinOutputs[i].UnlockHash)
	}
	for i := range txn.SiafundInputs {
		relevant = relevant || ownsAddress(txn.SiafundInputs[i].UnlockConditions.UnlockHash())
		relevant = relevant || ownsAddress(txn.SiafundInputs[i].ClaimUnlockHash)
	}
	for i := range txn.SiafundOutputs {
		relevant = relevant || ownsAddress(txn.SiafundOutputs[i].UnlockHash)
	}
	for i := range txn.FileContracts {
		relevant = relevant || relevantFileContract(txn.FileContracts[i], ownsAddress)
	}
	for i := range txn.FileContractRevisions {
		for _, sco := range txn.FileContractRevisions[i].NewValidProofOutputs {
			relevant = relevant || ownsAddress(sco.UnlockHash)
		}
		for _, sco := range txn.FileContractRevisions[i].NewMissedProofOutputs {
			relevant = relevant || ownsAddress(sco.UnlockHash)
		}
	}
	return relevant
}

func (s *EphemeralStore) ownsAddress(addr types.UnlockHash) bool {
	_, ok := s.addrs[addr]
	return ok
}

func (s *EphemeralStore) ProcessConsensusChange(cc modules.ConsensusChange) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sco := range cc.SiacoinOutputDiffs {
		if _, ok := s.addrs[sco.SiacoinOutput.UnlockHash]; !ok {
			continue
		}

		if sco.Direction == modules.DiffApply {
			s.outputsSC[sco.ID] = wallet.SiacoinElement{
				ID:             sco.ID,
				SiacoinOutput:  sco.SiacoinOutput,
				MaturityHeight: cc.BlockHeight,
			}
		} else {
			delete(s.outputsSC, sco.ID)
		}
	}

	for _, sco := range cc.DelayedSiacoinOutputDiffs {
		if _, ok := s.addrs[sco.SiacoinOutput.UnlockHash]; !ok {
			continue
		}

		if sco.Direction == modules.DiffApply {
			s.outputsSC[sco.ID] = wallet.SiacoinElement{
				ID:             sco.ID,
				SiacoinOutput:  sco.SiacoinOutput,
				MaturityHeight: sco.MaturityHeight,
			}
		} else {
			delete(s.outputsSC, sco.ID)
		}
	}

	for _, sfo := range cc.SiafundOutputDiffs {
		if _, ok := s.addrs[sfo.SiafundOutput.UnlockHash]; !ok {
			continue
		}

		if sfo.Direction == modules.DiffApply {
			s.outputsSF[sfo.ID] = wallet.SiafundElement{
				ID:            sfo.ID,
				SiafundOutput: sfo.SiafundOutput,
			}
		} else {
			delete(s.outputsSF, sfo.ID)
		}
	}

	for _, fco := range cc.FileContractDiffs {
		if !relevantFileContract(fco.FileContract, s.ownsAddress) {
			continue
		}
		if fco.Direction == modules.DiffApply {
			s.contracts[fco.ID] = wallet.FileContractElement{
				ID:           fco.ID,
				FileContract: fco.FileContract,
			}
		} else {
			delete(s.contracts, fco.ID)
		}
	}

	height := cc.InitialHeight()

	for _, block := range cc.RevertedBlocks {
		height--
		for _, txn := range block.Transactions {
			if !relevantTransaction(txn, s.ownsAddress) {
				continue
			}
			delete(s.txns, txn.ID())
		}
	}

	for _, block := range cc.AppliedBlocks {
		height++
		for _, txn := range block.Transactions {
			if !relevantTransaction(txn, s.ownsAddress) {
				continue
			}

			var inflow, outflow types.Currency
			for _, out := range txn.SiacoinOutputs {
				if _, ok := s.addrs[out.UnlockHash]; ok {
					inflow = inflow.Add(out.Value)
				}
			}
			for _, in := range txn.SiacoinInputs {
				if _, ok := s.addrs[in.UnlockConditions.UnlockHash()]; ok {
					outflow = outflow.Add(s.outputsSC[in.ParentID].Value)
				}
			}
			s.txns[txn.ID()] = wallet.Transaction{
				Raw:       txn,
				Index:     wallet.ChainIndex{block.ID(), height},
				ID:        txn.ID(),
				Inflow:    inflow,
				Outflow:   outflow,
				Timestamp: time.Unix(int64(block.Timestamp), 0),
			}
		}
	}

	s.ccid = cc.ID
	return
}

func (s *EphemeralStore) ConsensusChangeID() (modules.ConsensusChangeID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.ccid, nil
}

func (s *EphemeralStore) Transaction(id types.TransactionID) (wallet.Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	txn, ok := s.txns[id]
	if !ok {
		return wallet.Transaction{}, errors.New("no such transaction")
	}
	return txn, nil
}

func (s *EphemeralStore) Transactions(since time.Time, max int) (txns []wallet.Transaction, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, txn := range s.txns {
		if max == 0 {
			return
		} else if !txn.Timestamp.After(since) {
			continue
		}

		txns = append(txns, txn)
		max--
	}
	return
}

func (s *EphemeralStore) TransactionsByAddress(addr types.UnlockHash) (txns []wallet.Transaction, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ownsAddress := func(a types.UnlockHash) bool {
		return a == addr
	}
	for _, txn := range s.txns {
		if relevantTransaction(txn.Raw, ownsAddress) {
			txns = append(txns, txn)
		}
	}

	return
}

func (s *EphemeralStore) UnspentSiacoinOutputs() (outputs []wallet.SiacoinElement, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, output := range s.outputsSC {
		outputs = append(outputs, output)
	}
	return
}

func (s *EphemeralStore) UnspentSiafundOutputs() (outputs []wallet.SiafundElement, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, output := range s.outputsSF {
		outputs = append(outputs, output)
	}
	return
}

func (s *EphemeralStore) SeedIndex() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.seedIndex, nil
}

func (s *EphemeralStore) SetSeedIndex(index uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seedIndex = index
	return nil
}

func (s *EphemeralStore) AddressInfo(addr types.UnlockHash) (wallet.SeedAddressInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, ok := s.addrs[addr]
	if !ok {
		return wallet.SeedAddressInfo{}, errors.New("address does not exist")
	}
	return info, nil
}

func (s *EphemeralStore) AddAddress(info wallet.SeedAddressInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.addrs[info.UnlockConditions.UnlockHash()] = info
	if next := info.KeyIndex + 1; s.seedIndex < next {
		s.seedIndex = next
	}
	return nil
}

func (s *EphemeralStore) Addresses() (addrs []types.UnlockHash, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for addr := range s.addrs {
		addrs = append(addrs, addr)
	}
	return
}

func (s *EphemeralStore) DistributeFunds(n int, per, feePerByte types.Currency) (ins []wallet.SiacoinElement, fee, change types.Currency, err error) {
	utxos, err := s.UnspentSiacoinOutputs()
	if err != nil {
		return
	}
	ins, fee, change = wallet.DistributeFunds(utxos, n, per, feePerByte)
	return
}

func NewEphemeralStore() *EphemeralStore {
	return &EphemeralStore{
		addrs:     make(map[types.UnlockHash]wallet.SeedAddressInfo),
		outputsSC: make(map[types.SiacoinOutputID]wallet.SiacoinElement),
		outputsSF: make(map[types.SiafundOutputID]wallet.SiafundElement),
		contracts: make(map[types.FileContractID]wallet.FileContractElement),
		txns:      make(map[types.TransactionID]wallet.Transaction),
	}
}

// JSONStore implements wallet.Store in memory, backed by a JSON file.
type JSONStore struct {
	*EphemeralStore
	dir string
}

// ProcessConsensusChange implements wallet.Store.
func (s *JSONStore) ProcessConsensusChange(cc modules.ConsensusChange) {
	s.EphemeralStore.ProcessConsensusChange(cc)
	s.save()
}

// SetSetIndex implements wallet.Store.
func (s *JSONStore) SetSeedIndex(index uint64) error {
	if err := s.EphemeralStore.SetSeedIndex(index); err != nil {
		return err
	}
	return s.save()
}

// AddAddress implements wallet.Store.
func (s *JSONStore) AddAddress(info wallet.SeedAddressInfo) error {
	if err := s.EphemeralStore.AddAddress(info); err != nil {
		return err
	}
	return s.save()
}

func (s *JSONStore) load() error {
	dst := filepath.Join(s.dir, "wallet.json")
	f, err := os.Open(dst)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(f).Decode(&s)
}

func (s *JSONStore) save() error {
	js, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	dst := filepath.Join(s.dir, "wallet.json")
	f, err := os.OpenFile(dst+"_tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0660)
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
	} else if err := os.Rename(dst+"_tmp", dst); err != nil {
		return err
	}
	return nil
}

// NewJSONStore returns a new JSONStore.
func NewJSONStore(dir string) (*JSONStore, error) {
	s := &JSONStore{
		EphemeralStore: NewEphemeralStore(),
		dir:            dir,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}
