package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"lukechampine.com/frand"
)

func generateBlockTemplate(cm ChainManager, addr types.Address) (MiningGetBlockTemplateResponse, error) {
	block, cs := unsolvedBlock(cm, addr)

	// sanity check miner payouts
	if len(block.MinerPayouts) != 1 {
		return MiningGetBlockTemplateResponse{}, fmt.Errorf("expected 1 miner payout got %d", len(block.MinerPayouts))
	}

	// figure out encoding version
	version := uint32(1)
	if block.V2 != nil {
		version = 2
	}

	// encode payout
	buf := new(bytes.Buffer)
	enc := types.NewEncoder(buf)
	if block.V2 == nil {
		types.V1SiacoinOutput(block.MinerPayouts[0]).EncodeTo(enc)
	} else {
		types.V2SiacoinOutput(block.MinerPayouts[0]).EncodeTo(enc)
	}
	if err := enc.Flush(); err != nil {
		return MiningGetBlockTemplateResponse{}, err
	}
	minerPayout := MiningGetBlockTemplateResponseTxn{
		Data: hex.EncodeToString(buf.Bytes()),
	}

	// encode transactions
	var txns []MiningGetBlockTemplateResponseTxn
	for _, txn := range block.Transactions {
		buf.Reset()
		txn.EncodeTo(enc)
		if err := enc.Flush(); err != nil {
			return MiningGetBlockTemplateResponse{}, err
		}
		txns = append(txns, MiningGetBlockTemplateResponseTxn{
			Data:   hex.EncodeToString(buf.Bytes()),
			TxID:   txn.ID().String(),
			TxType: "1", // types.Transaction encoding
		})
	}
	for _, txn := range block.V2.Transactions {
		buf.Reset()
		txn.EncodeTo(enc)
		if err := enc.Flush(); err != nil {
			return MiningGetBlockTemplateResponse{}, err
		}
		txns = append(txns, MiningGetBlockTemplateResponseTxn{
			Data:   hex.EncodeToString(buf.Bytes()),
			TxID:   txn.ID().String(),
			TxType: "2", // types.V2Transaction encoding
		})
	}

	return MiningGetBlockTemplateResponse{
		Transactions: txns,
		MinerPayout:  []MiningGetBlockTemplateResponseTxn{minerPayout},
		PreviousHash: block.ParentID.String(),
		LongPollID:   hex.EncodeToString(frand.Bytes(16)),
		Target:       cs.ChildTarget.String(),
		Height:       uint32(cs.Index.Height) + 1,
		Timestamp:    int32(block.Timestamp.Unix()),
		Version:      version,
		Bits:         compressDifficulty(cs.Difficulty),
	}, nil
}

func compressDifficulty(w consensus.Work) string {
	buf := new(bytes.Buffer)
	enc := types.NewEncoder(buf)
	w.EncodeTo(enc)
	if err := enc.Flush(); err != nil {
		panic("failed to flush encoder") // can't fail
	}
	b := new(big.Int).SetBytes(buf.Bytes())
	return fmt.Sprintf("%08X", bigToCompact(b))
}

// bigToCompact converts a whole number N to a compact representation using an
// unsigned 32-bit number.
func bigToCompact(n *big.Int) uint32 {
	// No need to do any work if it's zero.
	if n.Sign() == 0 {
		return 0
	}

	// Since the base for the exponent is 256, the exponent can be treated
	// as the number of bytes.  So, shift the number right or left
	// accordingly.  This is equivalent to:
	// mantissa = mantissa / 256^(exponent-3)
	var mantissa uint32
	exponent := uint(len(n.Bytes()))
	if exponent <= 3 {
		mantissa = uint32(n.Bits()[0])
		mantissa <<= 8 * (3 - exponent)
	} else {
		// Use a copy to avoid modifying the caller's original number.
		tn := new(big.Int).Set(n)
		mantissa = uint32(tn.Rsh(tn, 8*(exponent-3)).Bits()[0])
	}

	// When the mantissa already has the sign bit set, the number is too
	// large to fit into the available 23-bits, so divide the number by 256
	// and increment the exponent accordingly.
	if mantissa&0x00800000 != 0 {
		mantissa >>= 8
		exponent++
	}

	// Pack the exponent, sign bit, and mantissa into an unsigned 32-bit
	// int and return it.
	compact := uint32(exponent<<24) | mantissa
	if n.Sign() < 0 {
		compact |= 0x00800000
	}
	return compact
}

// mineBlock constructs a block from the provided address and the transactions
// in the txpool, and attempts to find a nonce for it that meets the PoW target.
func mineBlock(ctx context.Context, cm ChainManager, addr types.Address) (types.Block, error) {
	b, cs := unsolvedBlock(cm, addr)
	factor := cs.NonceFactor()
	for b.ID().CmpWork(cs.ChildTarget) < 0 {
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
	}
	return b, nil
}

func unsolvedBlock(cm ChainManager, addr types.Address) (types.Block, consensus.State) {
retry:
	cs := cm.TipState()
	txns := cm.PoolTransactions()
	v2Txns := cm.V2PoolTransactions()
	if cs.Index != cm.Tip() {
		goto retry
	}

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
	return b, cs
}
