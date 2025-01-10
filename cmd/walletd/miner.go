package main

import (
	"fmt"
	"log"
	"math/big"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/walletd/api"
	"lukechampine.com/frand"
)

func runCPUMiner(c *api.Client, minerAddr types.Address, n int) {
	log.Println("Started mining into", minerAddr)
	start := time.Now()

	var blocksFound int
	for {
		if n >= 0 && blocksFound >= n {
			break
		}
		elapsed := time.Since(start)
		cs, err := c.ConsensusTipState()
		checkFatalError("failed to get consensus tip state:", err)
		d, _ := new(big.Int).SetString(cs.Difficulty.String(), 10)
		d.Mul(d, big.NewInt(int64(1+elapsed)))
		fmt.Printf("\rMining block %4v...(%.2f blocks/day), difficulty %v)", cs.Index.Height+1, float64(blocksFound)*float64(24*time.Hour)/float64(elapsed), cs.Difficulty)

		_, txns, v2txns, err := c.TxpoolTransactions()
		checkFatalError("failed to get pool transactions:", err)
		b := types.Block{
			ParentID:     cs.Index.ID,
			Nonce:        cs.NonceFactor() * frand.Uint64n(100),
			Timestamp:    types.CurrentTimestamp(),
			MinerPayouts: []types.SiacoinOutput{{Address: minerAddr, Value: cs.BlockReward()}},
			Transactions: txns,
		}
		for _, txn := range txns {
			b.MinerPayouts[0].Value = b.MinerPayouts[0].Value.Add(txn.TotalFees())
		}
		for _, txn := range v2txns {
			b.MinerPayouts[0].Value = b.MinerPayouts[0].Value.Add(txn.MinerFee)
		}
		if len(v2txns) > 0 || cs.Index.Height+1 >= cs.Network.HardforkV2.RequireHeight {
			b.V2 = &types.V2BlockData{
				Height:       cs.Index.Height + 1,
				Transactions: v2txns,
			}
			b.V2.Commitment = cs.Commitment(cs.TransactionsCommitment(b.Transactions, b.V2Transactions()), b.MinerPayouts[0].Address)
		}
		if !coreutils.FindBlockNonce(cs, &b, time.Minute) {
			continue
		}
		blocksFound++
		index := types.ChainIndex{Height: cs.Index.Height + 1, ID: b.ID()}
		tip, err := c.ConsensusTip()
		checkFatalError("failed to get consensus tip:", err)
		if tip != cs.Index {
			fmt.Printf("\nMined %v but tip changed, starting over\n", index)
		} else if err := c.SyncerBroadcastBlock(b); err != nil {
			fmt.Printf("\nMined invalid block: %v\n", err)
		} else if b.V2 == nil {
			fmt.Printf("\nFound v1 block %v\n", index)
		} else {
			fmt.Printf("\nFound v2 block %v\n", index)
		}
	}
}
