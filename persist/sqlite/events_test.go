package sqlite

import (
	"fmt"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/wallet"
	"go.uber.org/zap"
	"lukechampine.com/frand"
)

func runBenchmarkWalletEvents(b *testing.B, name string, addresses, eventsPerAddress int) {
	b.Run(name, func(b *testing.B) {
		db := newTestStore(b, WithLog(zap.NewNop()))

		w, err := db.AddWallet(wallet.Wallet{
			Name: "test",
		})
		if err != nil {
			b.Fatal(err)
		}

		for i := 0; i < addresses; i++ {
			addr := types.Address(frand.Entropy256())
			if err := db.AddWalletAddresses(w.ID, wallet.Address{Address: addr}); err != nil {
				b.Fatal(err)
			}

			err := db.transaction(func(tx *txn) error {
				utx := &updateTx{
					indexMode:         wallet.IndexModeFull,
					tx:                tx,
					relevantAddresses: make(map[types.Address]bool),
				}

				events := make([]wallet.Event, eventsPerAddress)
				for i := range events {
					events[i] = wallet.Event{
						ID:             types.Hash256(frand.Entropy256()),
						MaturityHeight: uint64(i + 1),
						Relevant:       []types.Address{addr},
						Type:           wallet.EventTypeV1Transaction,
						Data:           wallet.EventV1Transaction{},
					}
				}

				return utx.ApplyIndex(types.ChainIndex{
					Height: uint64(i + 1),
					ID:     types.BlockID(frand.Entropy256()),
				}, wallet.AppliedState{
					Events: events,
				})
			})
			if err != nil {
				b.Fatal(err)
			}
		}

		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			expectedEvents := eventsPerAddress * addresses
			if expectedEvents > 100 {
				expectedEvents = 100
			}

			events, err := db.WalletEvents(w.ID, 0, 100)
			if err != nil {
				b.Fatal(err)
			} else if len(events) != expectedEvents {
				b.Fatalf("expected %d events, got %d", expectedEvents, len(events))
			}
		}
	})
}

func BenchmarkWalletEvents(b *testing.B) {
	benchmarks := []struct {
		addresses        int
		eventsPerAddress int
	}{
		{1, 1},
		{1, 10},
		{1, 1000},
		{10, 1},
		{10, 1000},
		{10, 100000},
		{1000000, 0},
		{1000000, 1},
		{1000000, 10},
	}
	for _, bm := range benchmarks {
		totalTransactions := bm.addresses * bm.eventsPerAddress
		runBenchmarkWalletEvents(b, fmt.Sprintf("wallet with %d addresses and %d transactions", bm.addresses, totalTransactions), bm.addresses, bm.eventsPerAddress)
	}
}
