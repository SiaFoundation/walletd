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
	subscribers   []modules.ConsensusSetSubscriber
	dscos         map[types.BlockHeight][]modules.DelayedSiacoinOutputDiff
	filecontracts map[types.FileContractID]types.FileContract
	height        types.BlockHeight
}

func (m *mockCS) ConsensusSetSubscribe(s modules.ConsensusSetSubscriber, ccid modules.ConsensusChangeID, cancel <-chan struct{}) error {
	m.subscribers = append(m.subscribers, s)
	return nil
}

func (m *mockCS) sendTxn(txn types.Transaction) {
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
		AppliedBlocks: []types.Block{{
			Transactions: []types.Transaction{txn},
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
	for i := range m.subscribers {
		m.subscribers[i].ProcessConsensusChange(cc)
	}
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
	for i := range m.subscribers {
		m.subscribers[i].ProcessConsensusChange(cc)
	}
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
}

type scenarioTransaction struct {
	index   int
	outputs []types.SiacoinOutput
}

type scenario struct {
	initial      []types.SiacoinOutput
	transactions []scenarioTransaction
	description  string
}

type walletStats struct {
	// count of remaining UTXOs in the UTXO pool after the scenario
	utxoRemaining int
	// the mean count of UTXOs in the UTXO pool during the scenario
	utxoMean float64
	// count of UTXOs received throughout the scenario
	utxoReceived int
	// count of UTXOs spent from the UTXO pool
	utxoSpent int
	// count of change outputs created
	change int
	// minimum change output value
	changeMinimum types.Currency
	// mean change output value
	changeMean types.Currency
	// maximum change output value
	changeMaximum types.Currency
	// standard deviation of change output value
	changeStandardDeviation types.Currency
	// fees paid for all transactions
	fees types.Currency
	// minimum # of UTXOs selected for input
	utxoInputMinimum int
	// mean # of UTXOs selected for input
	utxoInputMean float64
	// maximum # of UTXOs selected for input
	utxoInputMaximum int
	// standard deviation of input set size
	inputSetSizeStandardDeviation float64
	// transactions
	transactions int
}

func TestSimulate(t *testing.T) {
	seeds := make([]wallet.Seed, 100)
	for i := 0; i < len(seeds); i++ {
		seeds[i] = wallet.NewSeed()
	}

	// TODO: convert past blocks into scenarios
	scenarios := []scenario{
		{
			initial: []types.SiacoinOutput{
				{types.SiacoinPrecision, wallet.StandardAddress(seeds[0].PublicKey(0).Key)},
			},
			transactions: []scenarioTransaction{{0, []types.SiacoinOutput{{UnlockHash: wallet.StandardAddress(seeds[1].PublicKey(0).Key), Value: types.SiacoinPrecision.Div64(3)}}}},
			description:  "example 1",
		},
	}

	for _, coinSelection := range []wallet.CoinSelection{wallet.Random, wallet.Bitcoin, wallet.SingleRandomDraw} {
		for _, scenario := range scenarios {
			cs := new(mockCS)

			wallets := make([]struct {
				w     *wallet.HotWallet
				stats walletStats
			}, len(seeds))
			for i := range seeds {
				store := walletutil.NewEphemeralStore()
				ccid, err := store.ConsensusChangeID()
				if err != nil {
					t.Fatal(err)
				}
				cs.ConsensusSetSubscribe(store, ccid, nil)
				wallets[i].w = wallet.NewHotWallet(store, seeds[i])
				wallets[i].stats.changeMinimum = types.NewCurrency64(math.MaxUint64)
			}

			t.Log("Testing with coin selection: ", coinSelection)
			for _, transaction := range scenario.transactions {
				for i := 0; i < len(wallets); i++ {
					utxos, err := wallets[i].w.UnspentSiacoinOutputs()
					if err != nil {
						t.Fatal(err)
					}
					wallets[i].stats.utxoMean += float64(len(utxos))
					wallets[i].stats.utxoReceived += len(utxos)
				}

				w := wallets[transaction.index].w
				// create and add addresses
				for i := 0; i < 10; i++ {
					if _, err := w.Address(); err != nil {
						t.Fatal(err)
					}
				}

				// establish initial utxos
				cs.sendTxn(types.Transaction{
					SiacoinOutputs: scenario.initial,
				})

				var amountSC types.Currency
				for _, out := range transaction.outputs {
					amountSC = amountSC.Add(out.Value)
				}

				txn := types.Transaction{SiacoinOutputs: transaction.outputs}
				toSign, _, err := w.FundTransaction(&txn, amountSC, types.ZeroCurrency, types.SiacoinPrecision.Div64(1000), coinSelection)
				if err != nil {
					t.Fatal(err)
				}
				if err := w.SignTransaction(&txn, toSign); err != nil {
					t.Fatal(err)
				}
				cs.sendTxn(txn)

				hasChange := (len(txn.SiacoinOutputs) - len(transaction.outputs)) > 0
				if hasChange {
					wallets[transaction.index].stats.change += 1

					change := txn.SiacoinOutputs[len(txn.SiacoinOutputs)-1].Value
					if change.Cmp(wallets[transaction.index].stats.changeMinimum) == -1 {
						wallets[transaction.index].stats.changeMinimum = change
					}
					if change.Cmp(wallets[transaction.index].stats.changeMinimum) == 1 {
						wallets[transaction.index].stats.changeMaximum = change
					}
					wallets[transaction.index].stats.changeMean = wallets[transaction.index].stats.changeMean.Add(change)
				}
				for _, fee := range txn.MinerFees {
					wallets[transaction.index].stats.fees = wallets[transaction.index].stats.fees.Add(fee)
				}

				if len(txn.SiacoinInputs) < wallets[transaction.index].stats.utxoInputMinimum {
					wallets[transaction.index].stats.utxoInputMinimum = len(txn.SiacoinInputs)
				}
				if len(txn.SiacoinInputs) > wallets[transaction.index].stats.utxoInputMaximum {
					wallets[transaction.index].stats.utxoInputMaximum = len(txn.SiacoinInputs)
				}
				wallets[transaction.index].stats.utxoInputMean += float64(len(txn.SiacoinInputs))

				wallets[transaction.index].stats.utxoSpent += len(txn.SiacoinInputs)
				wallets[transaction.index].stats.transactions++

				// we add len(utxos) at the beginning of the loop so this
				// represents the number of new utxos received
				for i := 0; i < len(wallets); i++ {
					utxos, err := wallets[i].w.UnspentSiacoinOutputs()
					if err != nil {
						t.Fatal(err)
					}
					wallets[i].stats.utxoReceived -= len(utxos)
				}
			}

			for i := range wallets {
				w := wallets[i].w

				utxos, err := w.UnspentSiacoinOutputs()
				if err != nil {
					t.Fatal(err)
				}

				wallets[i].stats.utxoRemaining = len(utxos)

				if wallets[i].stats.change > 0 {
					wallets[i].stats.changeMean = wallets[i].stats.changeMean.Div64(uint64(wallets[i].stats.change))
				}

				if len(scenario.transactions) > 0 {
					wallets[i].stats.utxoMean /= float64(wallets[i].stats.transactions)
					wallets[i].stats.utxoInputMean /= float64(wallets[i].stats.transactions)
				}
			}
			// record and output various statistics
		}
	}
}
