package wallet_test

import (
	"math"
	"sync"
	"testing"
	"time"

	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/types"
	"go.sia.tech/walletd/internal/walletutil"
	"go.sia.tech/walletd/wallet"
	"lukechampine.com/frand"
)

type mockCS struct {
	subscriber    modules.ConsensusSetSubscriber
	dscos         map[types.BlockHeight][]modules.DelayedSiacoinOutputDiff
	filecontracts map[types.FileContractID]types.FileContract
	height        types.BlockHeight
}

func (m *mockCS) ConsensusSetSubscribe(s modules.ConsensusSetSubscriber, ccid modules.ConsensusChangeID, cancel <-chan struct{}) error {
	m.subscriber = s
	return nil
}

func (m *mockCS) sendTxn(txn types.Transaction) {
	outputs := make([]modules.SiacoinOutputDiff, len(txn.SiacoinOutputs))
	for i := range outputs {
		outputs[i] = modules.SiacoinOutputDiff{
			Direction:     modules.DiffApply,
			SiacoinOutput: txn.SiacoinOutputs[i],
			ID:            txn.SiacoinOutputID(uint64(i)),
		}
	}
	cc := modules.ConsensusChange{
		AppliedBlocks: []types.Block{{
			Transactions: []types.Transaction{txn},
		}},
		ConsensusChangeDiffs: modules.ConsensusChangeDiffs{
			SiacoinOutputDiffs: outputs,
		},
		ID: frand.Entropy256(),
	}
	m.subscriber.ProcessConsensusChange(cc)
	m.height++
}

func (m *mockCS) mineBlock(fees types.Currency, addr types.UnlockHash) {
	b := types.Block{
		Transactions: []types.Transaction{{
			MinerFees: []types.Currency{fees},
		}},
		MinerPayouts: []types.SiacoinOutput{
			{UnlockHash: addr},
		},
	}
	b.MinerPayouts[0].Value = b.CalculateSubsidy(0)
	cc := modules.ConsensusChange{
		AppliedBlocks: []types.Block{b},
		ConsensusChangeDiffs: modules.ConsensusChangeDiffs{
			DelayedSiacoinOutputDiffs: []modules.DelayedSiacoinOutputDiff{{
				SiacoinOutput:  b.MinerPayouts[0],
				ID:             b.MinerPayoutID(0),
				MaturityHeight: types.MaturityDelay,
			}},
		},
		ID: frand.Entropy256(),
	}
	for _, dsco := range m.dscos[m.height] {
		cc.SiacoinOutputDiffs = append(cc.SiacoinOutputDiffs, modules.SiacoinOutputDiff{
			Direction:     modules.DiffApply,
			SiacoinOutput: dsco.SiacoinOutput,
			ID:            dsco.ID,
		})
	}
	m.subscriber.ProcessConsensusChange(cc)
	m.height++
	if m.dscos == nil {
		m.dscos = make(map[types.BlockHeight][]modules.DelayedSiacoinOutputDiff)
	}
	dsco := cc.DelayedSiacoinOutputDiffs[0]
	m.dscos[dsco.MaturityHeight] = append(m.dscos[dsco.MaturityHeight], dsco)
}

func (m *mockCS) formContract(payout types.Currency, addr types.UnlockHash) {
	b := types.Block{
		Transactions: []types.Transaction{{
			FileContracts: []types.FileContract{{
				Payout: payout,
				ValidProofOutputs: []types.SiacoinOutput{
					{UnlockHash: addr, Value: payout},
					{},
				},
				MissedProofOutputs: []types.SiacoinOutput{
					{UnlockHash: addr, Value: payout},
					{},
				},
			}},
		}},
	}
	cc := modules.ConsensusChange{
		AppliedBlocks: []types.Block{b},
		ConsensusChangeDiffs: modules.ConsensusChangeDiffs{
			FileContractDiffs: []modules.FileContractDiff{{
				FileContract: b.Transactions[0].FileContracts[0],
				ID:           b.Transactions[0].FileContractID(0),
				Direction:    modules.DiffApply,
			}},
		},
		ID: frand.Entropy256(),
	}
	m.subscriber.ProcessConsensusChange(cc)
	m.height++
	if m.filecontracts == nil {
		m.filecontracts = make(map[types.FileContractID]types.FileContract)
	}
	m.filecontracts[b.Transactions[0].FileContractID(0)] = b.Transactions[0].FileContracts[0]
}

func (m *mockCS) reviseContract(id types.FileContractID) {
	fc := m.filecontracts[id]
	delta := fc.ValidProofOutputs[0].Value.Div64(2)
	fc.ValidProofOutputs[0].Value = fc.ValidProofOutputs[0].Value.Sub(delta)
	fc.ValidProofOutputs[1].Value = fc.ValidProofOutputs[1].Value.Add(delta)
	fc.MissedProofOutputs[0].Value = fc.MissedProofOutputs[0].Value.Sub(delta)
	fc.MissedProofOutputs[1].Value = fc.MissedProofOutputs[1].Value.Add(delta)
	fc.RevisionNumber++
	b := types.Block{
		Transactions: []types.Transaction{{
			FileContractRevisions: []types.FileContractRevision{{
				ParentID:              id,
				NewFileSize:           fc.FileSize,
				NewFileMerkleRoot:     fc.FileMerkleRoot,
				NewWindowStart:        fc.WindowStart,
				NewWindowEnd:          fc.WindowEnd,
				NewValidProofOutputs:  fc.ValidProofOutputs,
				NewMissedProofOutputs: fc.MissedProofOutputs,
				NewUnlockHash:         fc.UnlockHash,
				NewRevisionNumber:     fc.RevisionNumber,
			}},
		}},
	}
	cc := modules.ConsensusChange{
		AppliedBlocks: []types.Block{b},
		ConsensusChangeDiffs: modules.ConsensusChangeDiffs{
			FileContractDiffs: []modules.FileContractDiff{
				{
					FileContract: m.filecontracts[id],
					ID:           id,
					Direction:    modules.DiffRevert,
				},
				{
					FileContract: fc,
					ID:           id,
					Direction:    modules.DiffApply,
				},
			},
		},
		ID: frand.Entropy256(),
	}
	m.subscriber.ProcessConsensusChange(cc)
	m.height++
	m.filecontracts[id] = fc
}

func TestHotWallet(t *testing.T) {
	store := walletutil.NewEphemeralStore()
	w := wallet.NewHotWallet(store, wallet.NewSeed())
	cs := new(mockCS)
	ccid, err := store.ConsensusChangeID()
	if err != nil {
		t.Fatal(err)
	}
	cs.ConsensusSetSubscribe(store, ccid, nil)

	// initial balance should be zero
	sc, _, err := w.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if !sc.IsZero() {
		t.Fatal("balance should be zero")
	}

	// shouldn't have any transactions yet
	txnHistory, err := w.Transactions(time.Time{}, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if len(txnHistory) != 0 {
		t.Fatal("transaction history should be empty")
	}

	// shouldn't have any addresses yet
	addresses, err := w.Addresses()
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 0 {
		t.Fatal("address list should be empty")
	}

	// create and add an address
	addr, err := w.Address()
	if err != nil {
		t.Fatal(err)
	}

	// should have an address now
	addresses, err = w.Addresses()
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 || addresses[0] != addr {
		t.Fatal("bad address list", addresses)
	}

	// simulate a transaction
	cs.sendTxn(types.Transaction{
		SiacoinOutputs: []types.SiacoinOutput{
			{UnlockHash: addr, Value: types.SiacoinPrecision.Div64(2)},
			{UnlockHash: addr, Value: types.SiacoinPrecision.Div64(2)},
		},
	})

	// get new balance
	sc, _, err = w.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if !sc.Equals(types.SiacoinPrecision) {
		t.Fatal("balance should be 1 SC")
	}

	// transaction should appear in history
	txnHistory, err = w.TransactionsByAddress(addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(txnHistory) != 1 {
		t.Fatal("transaction should appear in history")
	}
	htx, err := w.Transaction(txnHistory[0].ID)
	if err != nil {
		t.Fatal(err)
	} else if len(htx.Raw.SiacoinOutputs) != 2 {
		t.Fatal("transaction should have two outputs")
	} else if htx.Index.Height != 1 {
		t.Fatal("transaction height should be 1")
	}

	outputs, err := w.UnspentSiacoinOutputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 {
		t.Fatal("should have two UTXOs")
	}
}

func TestHotWalletThreadSafety(t *testing.T) {
	store := walletutil.NewEphemeralStore()
	w := wallet.NewHotWallet(store, wallet.NewSeed())
	cs := new(mockCS)
	ccid, err := store.ConsensusChangeID()
	if err != nil {
		t.Fatal(err)
	}
	cs.ConsensusSetSubscribe(store, ccid, nil)

	addr, err := w.Address()
	if err != nil {
		t.Fatal(err)
	}
	txn := types.Transaction{
		SiacoinOutputs: []types.SiacoinOutput{
			{UnlockHash: addr, Value: types.SiacoinPrecision.Div64(2)},
		},
	}

	// create a bunch of goroutines that call routes and add transactions
	// concurrently
	funcs := []func(){
		func() { cs.sendTxn(txn) },
		func() { w.Balance() },
		func() { w.Address() },
		func() { w.Addresses() },
		func() { w.Transactions(time.Time{}, math.MaxInt64) },
	}
	var wg sync.WaitGroup
	wg.Add(len(funcs))
	for _, fn := range funcs {
		go func(fn func()) {
			for i := 0; i < 10; i++ {
				time.Sleep(time.Duration(frand.Intn(10)) * time.Millisecond)
				fn()
			}
			wg.Done()
		}(fn)
	}
	wg.Wait()
}
