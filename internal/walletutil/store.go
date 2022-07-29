package walletutil

import (
	"time"

	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
	"go.sia.tech/walletd/wallet"
)

type EphemeralStore struct {
	ccid modules.ConsensusChangeID

	addrs map[types.UnlockHash]wallet.SeedAddressInfo

	scOutputs map[types.SiacoinOutputID]wallet.SiacoinElement
	sfOutputs map[types.SiafundOutputID]wallet.SiafundElement
	contracts map[types.FileContractID]wallet.FileContractElement

	txns map[types.TransactionID]wallet.Transaction

	seedIndex uint64
}

func (s *EphemeralStore) ownsAddress(addr types.UnlockHash) bool {
	_, ok := s.addrs[addr]
	return ok
}

func (s *EphemeralStore) ProcessConsensusChange(cc modules.ConsensusChange) {
	for _, sco := range cc.SiacoinOutputDiffs {
		if _, ok := s.addrs[sco.SiacoinOutput.UnlockHash]; !ok {
			continue
		}

		if sco.Direction == modules.DiffApply {
			s.scOutputs[sco.ID] = wallet.SiacoinElement{
				ID:             sco.ID,
				SiacoinOutput:  sco.SiacoinOutput,
				MaturityHeight: cc.BlockHeight,
			}
		} else {
			delete(s.scOutputs, sco.ID)
		}
	}

	for _, sco := range cc.DelayedSiacoinOutputDiffs {
		if _, ok := s.addrs[sco.SiacoinOutput.UnlockHash]; !ok {
			continue
		}

		if sco.Direction == modules.DiffApply {
			s.scOutputs[sco.ID] = wallet.SiacoinElement{
				ID:             sco.ID,
				SiacoinOutput:  sco.SiacoinOutput,
				MaturityHeight: sco.MaturityHeight,
			}
		} else {
			delete(s.scOutputs, sco.ID)
		}
	}

	for _, sfo := range cc.SiafundOutputDiffs {
		if _, ok := s.addrs[sfo.SiafundOutput.UnlockHash]; !ok {
			continue
		}

		if sfo.Direction == modules.DiffApply {
			s.sfOutputs[sfo.ID] = wallet.SiafundElement{
				ID:            sfo.ID,
				SiafundOutput: sfo.SiafundOutput,
			}
		} else {
			delete(s.sfOutputs, sfo.ID)
		}
	}

	relevantFileContract := func(fc types.FileContract) bool {
		relevant := false
		for _, sco := range fc.ValidProofOutputs {
			relevant = relevant || s.ownsAddress(sco.UnlockHash)
		}
		for _, sco := range fc.MissedProofOutputs {
			relevant = relevant || s.ownsAddress(sco.UnlockHash)
		}
		return relevant
	}
	relevantTransaction := func(txn types.Transaction) bool {
		relevant := false
		for i := range txn.SiacoinInputs {
			relevant = relevant || s.ownsAddress(txn.SiacoinInputs[i].UnlockConditions.UnlockHash())
		}
		for i := range txn.SiacoinOutputs {
			relevant = relevant || s.ownsAddress(txn.SiacoinOutputs[i].UnlockHash)
		}
		for i := range txn.SiafundInputs {
			relevant = relevant || s.ownsAddress(txn.SiafundInputs[i].UnlockConditions.UnlockHash())
			relevant = relevant || s.ownsAddress(txn.SiafundInputs[i].ClaimUnlockHash)
		}
		for i := range txn.SiafundOutputs {
			relevant = relevant || s.ownsAddress(txn.SiafundOutputs[i].UnlockHash)
		}
		for i := range txn.FileContracts {
			relevant = relevant || relevantFileContract(txn.FileContracts[i])
		}
		for i := range txn.FileContractRevisions {
			for _, sco := range txn.FileContractRevisions[i].NewValidProofOutputs {
				relevant = relevant || s.ownsAddress(sco.UnlockHash)
			}
			for _, sco := range txn.FileContractRevisions[i].NewMissedProofOutputs {
				relevant = relevant || s.ownsAddress(sco.UnlockHash)
			}
		}
		return relevant
	}

	for _, fco := range cc.FileContractDiffs {
		if !relevantFileContract(fco.FileContract) {
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
			if !relevantTransaction(txn) {
				continue
			}
			delete(s.txns, txn.ID())
		}
	}

	for _, block := range cc.AppliedBlocks {
		height++
		for _, txn := range block.Transactions {
			if !relevantTransaction(txn) {
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
					outflow = outflow.Add(s.scOutputs[in.ParentID].Value)
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
	return s.ccid, nil
}

func (s *EphemeralStore) Transaction(id types.TransactionID) (wallet.Transaction, bool, error) {
	txn, ok := s.txns[id]
	return txn, ok, nil
}

func (s *EphemeralStore) Transactions(since time.Time, max int) (txns []wallet.Transaction, err error) {
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

func (s *EphemeralStore) UnspentOutputs() (outputs []wallet.SiacoinElement, err error) {
	for _, output := range s.scOutputs {
		outputs = append(outputs, output)
	}
	return
}

func (s *EphemeralStore) SeedIndex() (uint64, error) {
	return s.seedIndex, nil
}

func (s *EphemeralStore) SetSeedIndex(index uint64) error {
	s.seedIndex = index
	return nil
}

func (s *EphemeralStore) AddAddress(info wallet.SeedAddressInfo) error {
	s.addrs[info.UnlockConditions.UnlockHash()] = info
	if next := info.KeyIndex + 1; s.seedIndex < next {
		s.seedIndex = next
	}
	return nil
}

func NewEphemeralStore() *EphemeralStore {
	return &EphemeralStore{
		addrs:     make(map[types.UnlockHash]wallet.SeedAddressInfo),
		scOutputs: make(map[types.SiacoinOutputID]wallet.SiacoinElement),
		sfOutputs: make(map[types.SiafundOutputID]wallet.SiafundElement),
		contracts: make(map[types.FileContractID]wallet.FileContractElement),
		txns:      make(map[types.TransactionID]wallet.Transaction),
	}
}
