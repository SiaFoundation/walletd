package walletutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/encoding"
	"go.sia.tech/core/types"
	"go.sia.tech/siad/modules"
	"go.sia.tech/walletd/wallet"
)

type EphemeralStore struct {
	seedIndex uint64
	ccid      modules.ConsensusChangeID

	addrs     map[types.Address]wallet.SeedAddressInfo
	txns      map[types.TransactionID]wallet.Transaction
	outputsSC map[types.SiacoinOutputID]wallet.SiacoinElement
	outputsSF map[types.SiafundOutputID]wallet.SiafundElement
	contracts map[types.FileContractID]wallet.FileContractElement

	mu sync.Mutex
}

func relevantFileContract(fc types.FileContract, ownsAddress func(types.Address) bool) bool {
	relevant := false
	for _, sco := range fc.ValidProofOutputs {
		relevant = relevant || ownsAddress(sco.Address)
	}
	for _, sco := range fc.MissedProofOutputs {
		relevant = relevant || ownsAddress(sco.Address)
	}
	return relevant
}

func relevantTransaction(txn types.Transaction, ownsAddress func(types.Address) bool) bool {
	relevant := false
	for i := range txn.SiacoinInputs {
		relevant = relevant || ownsAddress(txn.SiacoinInputs[i].UnlockConditions.UnlockHash())
	}
	for i := range txn.SiacoinOutputs {
		relevant = relevant || ownsAddress(txn.SiacoinOutputs[i].Address)
	}
	for i := range txn.SiafundInputs {
		relevant = relevant || ownsAddress(txn.SiafundInputs[i].UnlockConditions.UnlockHash())
		relevant = relevant || ownsAddress(txn.SiafundInputs[i].ClaimAddress)
	}
	for i := range txn.SiafundOutputs {
		relevant = relevant || ownsAddress(txn.SiafundOutputs[i].Address)
	}
	for i := range txn.FileContracts {
		relevant = relevant || relevantFileContract(txn.FileContracts[i], ownsAddress)
	}
	for i := range txn.FileContractRevisions {
		for _, sco := range txn.FileContractRevisions[i].ValidProofOutputs {
			relevant = relevant || ownsAddress(sco.Address)
		}
		for _, sco := range txn.FileContractRevisions[i].MissedProofOutputs {
			relevant = relevant || ownsAddress(sco.Address)
		}
	}
	return relevant
}

func siadConvertToCore(from interface{}, to types.DecoderFrom) {
	d := types.NewBufDecoder(encoding.Marshal(from))
	to.DecodeFrom(d)
	if err := d.Err(); err != nil {
		panic(fmt.Sprintf("type conversion failed (%T->%T): %v", from, to, err))
	}
}

func coreConvertToSiad(from types.EncoderTo, to interface{}) {
	var buf bytes.Buffer
	e := types.NewEncoder(&buf)
	from.EncodeTo(e)
	e.Flush()
	if err := encoding.Unmarshal(buf.Bytes(), to); err != nil {
		panic(fmt.Sprintf("type conversion failed (%T->%T): %v", from, to, err))
	}
}

func (s *EphemeralStore) ownsAddress(addr types.Address) bool {
	_, ok := s.addrs[addr]
	return ok
}

func (s *EphemeralStore) ProcessConsensusChange(cc modules.ConsensusChange) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sco := range cc.SiacoinOutputDiffs {
		var elem wallet.SiacoinElement
		siadConvertToCore(sco.ID, &elem.ID)
		if _, ok := s.outputsSC[elem.ID]; !ok && !s.ownsAddress(types.Address(sco.SiacoinOutput.UnlockHash)) {
			continue
		}

		if sco.Direction == modules.DiffApply {
			siadConvertToCore(sco.SiacoinOutput, &elem.SiacoinOutput)
			elem.MaturityHeight = uint64(cc.BlockHeight)
			s.outputsSC[elem.ID] = elem
		} else {
			delete(s.outputsSC, elem.ID)
		}
	}

	for _, sco := range cc.DelayedSiacoinOutputDiffs {
		var elem wallet.SiacoinElement
		siadConvertToCore(sco.ID, &elem.ID)
		if _, ok := s.outputsSC[elem.ID]; !ok && !s.ownsAddress(types.Address(sco.SiacoinOutput.UnlockHash)) {
			continue
		}

		if sco.Direction == modules.DiffApply {
			siadConvertToCore(sco.SiacoinOutput, &elem.SiacoinOutput)
			elem.MaturityHeight = uint64(sco.MaturityHeight)
			s.outputsSC[elem.ID] = elem
		} else {
			delete(s.outputsSC, elem.ID)
		}
	}

	for _, sfo := range cc.SiafundOutputDiffs {
		var elem wallet.SiafundElement
		siadConvertToCore(sfo.ID, &elem.ID)
		if _, ok := s.outputsSF[elem.ID]; !ok && !s.ownsAddress(types.Address(sfo.SiafundOutput.UnlockHash)) {
			continue
		}

		if sfo.Direction == modules.DiffApply {
			siadConvertToCore(sfo.SiafundOutput, &elem.SiafundOutput)
			s.outputsSF[elem.ID] = elem
		} else {
			delete(s.outputsSF, elem.ID)
		}
	}

	for _, fco := range cc.FileContractDiffs {
		var elem wallet.FileContractElement
		siadConvertToCore(fco.ID, &elem.ID)
		siadConvertToCore(fco.FileContract, &elem.FileContract)
		if !relevantFileContract(elem.FileContract, s.ownsAddress) {
			continue
		}

		if fco.Direction == modules.DiffApply {
			s.contracts[elem.ID] = elem
		} else {
			delete(s.contracts, elem.ID)
		}
	}

	height := cc.InitialHeight()

	for _, block := range cc.RevertedBlocks {
		height--
		for _, txn := range block.Transactions {
			var coreTxn types.Transaction
			siadConvertToCore(txn, &coreTxn)
			if !relevantTransaction(coreTxn, s.ownsAddress) {
				continue
			}
			delete(s.txns, coreTxn.ID())
		}
	}

	for _, block := range cc.AppliedBlocks {
		height++
		for _, txn := range block.Transactions {
			var coreTxn types.Transaction
			siadConvertToCore(txn, &coreTxn)
			if !relevantTransaction(coreTxn, s.ownsAddress) {
				continue
			}

			var inflow, outflow types.Currency
			for _, out := range coreTxn.SiacoinOutputs {
				if _, ok := s.addrs[out.Address]; ok {
					inflow = inflow.Add(out.Value)
				}
			}
			for _, in := range coreTxn.SiacoinInputs {
				if _, ok := s.addrs[in.UnlockConditions.UnlockHash()]; ok {
					outflow = outflow.Add(s.outputsSC[in.ParentID].Value)
				}
			}

			var coreBID types.BlockID
			siadConvertToCore(block.ID(), &coreBID)

			s.txns[coreTxn.ID()] = wallet.Transaction{
				Raw:       coreTxn,
				Index:     wallet.ChainIndex{coreBID, uint64(height)},
				ID:        coreTxn.ID(),
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

func (s *EphemeralStore) TransactionsByAddress(addr types.Address) (txns []wallet.Transaction, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ownsAddress := func(a types.Address) bool {
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

func (s *EphemeralStore) AddressInfo(addr types.Address) (wallet.SeedAddressInfo, error) {
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

func (s *EphemeralStore) Addresses() (addrs []types.Address, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for addr := range s.addrs {
		addrs = append(addrs, addr)
	}
	return
}

func NewEphemeralStore() *EphemeralStore {
	return &EphemeralStore{
		addrs:     make(map[types.Address]wallet.SeedAddressInfo),
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
