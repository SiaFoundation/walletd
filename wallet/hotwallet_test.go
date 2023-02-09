package wallet_test

import (
	"bytes"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/encoding"
	ctypes "go.sia.tech/core/types"
	"go.sia.tech/siad/modules"
	stypes "go.sia.tech/siad/types"
	"go.sia.tech/walletd/internal/walletutil"
	"go.sia.tech/walletd/wallet"
	"lukechampine.com/frand"
)

type mockCS struct {
	subscribers   []modules.ConsensusSetSubscriber
	dscos         map[stypes.BlockHeight][]modules.DelayedSiacoinOutputDiff
	filecontracts map[stypes.FileContractID]stypes.FileContract
	height        stypes.BlockHeight
}

func coreConvertToSiad(from ctypes.EncoderTo, to interface{}) {
	var buf bytes.Buffer
	e := ctypes.NewEncoder(&buf)
	from.EncodeTo(e)
	e.Flush()
	if err := encoding.Unmarshal(buf.Bytes(), to); err != nil {
		panic(fmt.Sprintf("type conversion failed (%T->%T): %v", from, to, err))
	}
}

func (m *mockCS) ConsensusSetSubscribe(s modules.ConsensusSetSubscriber, ccid modules.ConsensusChangeID, cancel <-chan struct{}) error {
	m.subscribers = append(m.subscribers, s)
	return nil
}

func (m *mockCS) sendTxn(ctxn ctypes.Transaction) {
	var txn stypes.Transaction
	coreConvertToSiad(ctxn, &txn)

	outputs := make([]modules.SiacoinOutputDiff, 0, len(txn.SiacoinOutputs)+len(txn.SiacoinInputs))
	for i, out := range txn.SiacoinOutputs {
		outputs = append(outputs, modules.SiacoinOutputDiff{
			Direction:     modules.DiffApply,
			SiacoinOutput: out,
			ID:            txn.SiacoinOutputID(uint64(i)),
		})
	}
	for _, in := range txn.SiacoinInputs {
		outputs = append(outputs, modules.SiacoinOutputDiff{
			Direction: modules.DiffRevert,
			ID:        in.ParentID,
		})
	}
	cc := modules.ConsensusChange{
		AppliedBlocks: []stypes.Block{{
			Transactions: []stypes.Transaction{txn},
		}},
		ConsensusChangeDiffs: modules.ConsensusChangeDiffs{
			SiacoinOutputDiffs: outputs,
		},
		ID: frand.Entropy256(),
	}
	for i := range m.subscribers {
		m.subscribers[i].ProcessConsensusChange(cc)
	}
	m.height++
}

func (m *mockCS) mineBlock(fees stypes.Currency, addr stypes.UnlockHash) {
	b := stypes.Block{
		Transactions: []stypes.Transaction{{
			MinerFees: []stypes.Currency{fees},
		}},
		MinerPayouts: []stypes.SiacoinOutput{
			{UnlockHash: addr},
		},
	}
	b.MinerPayouts[0].Value = b.CalculateSubsidy(0)
	cc := modules.ConsensusChange{
		AppliedBlocks: []stypes.Block{b},
		ConsensusChangeDiffs: modules.ConsensusChangeDiffs{
			DelayedSiacoinOutputDiffs: []modules.DelayedSiacoinOutputDiff{{
				SiacoinOutput:  b.MinerPayouts[0],
				ID:             b.MinerPayoutID(0),
				MaturityHeight: stypes.MaturityDelay,
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
	for i := range m.subscribers {
		m.subscribers[i].ProcessConsensusChange(cc)
	}
	m.height++
	if m.dscos == nil {
		m.dscos = make(map[stypes.BlockHeight][]modules.DelayedSiacoinOutputDiff)
	}
	dsco := cc.DelayedSiacoinOutputDiffs[0]
	m.dscos[dsco.MaturityHeight] = append(m.dscos[dsco.MaturityHeight], dsco)
}

func (m *mockCS) reviseContract(id stypes.FileContractID) {
	fc := m.filecontracts[id]
	delta := fc.ValidProofOutputs[0].Value.Div64(2)
	fc.ValidProofOutputs[0].Value = fc.ValidProofOutputs[0].Value.Sub(delta)
	fc.ValidProofOutputs[1].Value = fc.ValidProofOutputs[1].Value.Add(delta)
	fc.MissedProofOutputs[0].Value = fc.MissedProofOutputs[0].Value.Sub(delta)
	fc.MissedProofOutputs[1].Value = fc.MissedProofOutputs[1].Value.Add(delta)
	fc.RevisionNumber++
	b := stypes.Block{
		Transactions: []stypes.Transaction{{
			FileContractRevisions: []stypes.FileContractRevision{{
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
		AppliedBlocks: []stypes.Block{b},
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
	for i := range m.subscribers {
		m.subscribers[i].ProcessConsensusChange(cc)
	}
	m.height++
	m.filecontracts[id] = fc
}

func TestHotWallet(t *testing.T) {
	store1 := walletutil.NewEphemeralStore()
	w1 := wallet.NewHotWallet(store1, wallet.NewSeed())
	cs1 := new(mockCS)
	ccid1, err := store1.ConsensusChangeID()
	if err != nil {
		t.Fatal(err)
	}
	cs1.ConsensusSetSubscribe(store1, ccid1, nil)

	store2, err := walletutil.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w2 := wallet.NewHotWallet(store2, wallet.NewSeed())
	cs2 := new(mockCS)
	ccid2, err := store2.ConsensusChangeID()
	if err != nil {
		t.Fatal(err)
	}
	cs2.ConsensusSetSubscribe(store2, ccid2, nil)

	for _, s := range []struct {
		w  *wallet.HotWallet
		cs *mockCS
	}{{w1, cs1}, {w2, cs2}} {
		w := s.w
		cs := s.cs
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
		cs.sendTxn(ctypes.Transaction{
			SiacoinOutputs: []ctypes.SiacoinOutput{
				{Address: addr, Value: ctypes.Siacoins(1).Div64(2)},
				{Address: addr, Value: ctypes.Siacoins(1).Div64(2)},
			},
		})

		// get new balance
		sc, _, err = w.Balance()
		if err != nil {
			t.Fatal(err)
		}
		if !sc.Equals(ctypes.Siacoins(1)) {
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
}

func TestHotWalletThreadSafety(t *testing.T) {
	store1 := walletutil.NewEphemeralStore()
	w1 := wallet.NewHotWallet(store1, wallet.NewSeed())
	cs1 := new(mockCS)
	ccid1, err := store1.ConsensusChangeID()
	if err != nil {
		t.Fatal(err)
	}
	cs1.ConsensusSetSubscribe(store1, ccid1, nil)

	store2, err := walletutil.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w2 := wallet.NewHotWallet(store2, wallet.NewSeed())
	cs2 := new(mockCS)
	ccid2, err := store2.ConsensusChangeID()
	if err != nil {
		t.Fatal(err)
	}
	cs2.ConsensusSetSubscribe(store2, ccid2, nil)

	for _, s := range []struct {
		w  *wallet.HotWallet
		cs *mockCS
	}{{w1, cs1}, {w2, cs2}} {
		w := s.w
		cs := s.cs

		addr, err := w.Address()
		if err != nil {
			t.Fatal(err)
		}
		txn := ctypes.Transaction{
			SiacoinOutputs: []ctypes.SiacoinOutput{
				{Address: addr, Value: ctypes.Siacoins(1).Div64(2)},
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
}
