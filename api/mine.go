package api

import (
	"context"
	"encoding/binary"
	"errors"

	"go.sia.tech/core/types"
)

// mineBlock constructs a block from the provided address and the transactions
// in the txpool, and attempts to find a nonce for it that meets the PoW target.
func mineBlock(ctx context.Context, cm ChainManager, addr types.Address) (types.Block, error) {
	cs := cm.TipState()
	txns := cm.PoolTransactions()
	v2Txns := cm.V2PoolTransactions()

	b := types.Block{
		ParentID:  cs.Index.ID,
		Timestamp: types.CurrentTimestamp(),
		MinerPayouts: []types.SiacoinOutput{{
			Value:   cs.BlockReward(),
			Address: addr,
		}},
	}

	if cs.Index.Height >= cs.Network.HardforkV2.AllowHeight {
		b.V2 = &types.V2BlockData{
			Height: cs.Index.Height + 1,
		}
	}

	var weight uint64
	for _, txn := range txns {
		if weight += cs.TransactionWeight(txn); weight > cs.MaxBlockWeight() {
			break
		}
		b.Transactions = append(b.Transactions, txn)
		b.MinerPayouts[0].Value = b.MinerPayouts[0].Value.Add(txn.TotalFees())
	}
	for _, txn := range v2Txns {
		if weight += cs.V2TransactionWeight(txn); weight > cs.MaxBlockWeight() {
			break
		}
		b.V2.Transactions = append(b.V2.Transactions, txn)
		b.MinerPayouts[0].Value = b.MinerPayouts[0].Value.Add(txn.MinerFee)
	}
	if b.V2 != nil {
		b.V2.Commitment = cs.Commitment(cs.TransactionsCommitment(b.Transactions, b.V2Transactions()), addr)
	}

	b.Nonce = 0
	buf := make([]byte, 32+8+8+32)
	binary.LittleEndian.PutUint64(buf[32:], b.Nonce)
	binary.LittleEndian.PutUint64(buf[40:], uint64(b.Timestamp.Unix()))
	if b.V2 != nil {
		copy(buf[:32], "sia/id/block|")
		copy(buf[48:], b.V2.Commitment[:])
	} else {
		root := b.MerkleRoot()
		copy(buf[:32], b.ParentID[:])
		copy(buf[48:], root[:])
	}
	factor := cs.NonceFactor()
	for types.BlockID(types.HashBytes(buf)).CmpWork(cs.ChildTarget) < 0 {
		select {
		case <-ctx.Done():
			return types.Block{}, ctx.Err()
		default:
		}

		// tip changed, abort mining
		if cm.Tip() != cs.Index {
			return types.Block{}, errors.New("tip changed")
		}

		b.Nonce += factor
		binary.LittleEndian.PutUint64(buf[32:], b.Nonce)
	}
	return b, nil
}
