package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math/bits"
	"path/filepath"
	"sort"

	bolt "go.etcd.io/bbolt"
	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
)

// copied from types/multiproof.go

type elementLeaf struct {
	*types.StateElement
	ElementHash types.Hash256
}

func (l elementLeaf) hash() types.Hash256 {
	buf := make([]byte, 1+32+8+1)
	buf[0] = 0x00 // leafHashPrefix
	copy(buf[1:], l.ElementHash[:])
	binary.LittleEndian.PutUint64(buf[33:], l.LeafIndex)
	buf[41] = 0 // spent (always false for multiproofs)
	return types.HashBytes(buf)
}

func hashAll(elems ...interface{}) [32]byte {
	h := types.NewHasher()
	for _, e := range elems {
		if et, ok := e.(types.EncoderTo); ok {
			et.EncodeTo(h.E)
		} else {
			switch e := e.(type) {
			case string:
				h.WriteDistinguisher(e)
			case uint64:
				h.E.WriteUint64(e)
			}
		}
	}
	return h.Sum()
}

func chainIndexLeaf(e *types.ChainIndexElement) elementLeaf {
	return elementLeaf{&e.StateElement, hashAll("leaf/chainindex", e.ID, e.ChainIndex)}
}

func siacoinLeaf(e *types.SiacoinElement) elementLeaf {
	return elementLeaf{&e.StateElement, hashAll("leaf/siacoin", e.ID, e.SiacoinOutput, e.MaturityHeight)}
}

func siafundLeaf(e *types.SiafundElement) elementLeaf {
	return elementLeaf{&e.StateElement, hashAll("leaf/siafund", e.ID, e.SiafundOutput, e.ClaimStart)}
}

func v2FileContractLeaf(e *types.V2FileContractElement) elementLeaf {
	return elementLeaf{&e.StateElement, hashAll("leaf/v2filecontract", e.ID, e.V2FileContract)}
}

func splitLeaves(ls []elementLeaf, mid uint64) (left, right []elementLeaf) {
	split := sort.Search(len(ls), func(i int) bool { return ls[i].LeafIndex >= mid })
	return ls[:split], ls[split:]
}

func forEachElementLeaf(txns []types.V2Transaction, fn func(l elementLeaf)) {
	visit := func(l elementLeaf) {
		if l.LeafIndex != types.EphemeralLeafIndex {
			fn(l)
		}
	}
	for _, txn := range txns {
		for i := range txn.SiacoinInputs {
			visit(siacoinLeaf(&txn.SiacoinInputs[i].Parent))
		}
		for i := range txn.SiafundInputs {
			visit(siafundLeaf(&txn.SiafundInputs[i].Parent))
		}
		for i := range txn.FileContractRevisions {
			visit(v2FileContractLeaf(&txn.FileContractRevisions[i].Parent))
		}
		for i := range txn.FileContractResolutions {
			visit(v2FileContractLeaf(&txn.FileContractResolutions[i].Parent))
			if r, ok := txn.FileContractResolutions[i].Resolution.(*types.V2StorageProof); ok {
				visit(chainIndexLeaf(&r.ProofIndex))
			}
		}
	}
}

func forEachTree(txns []types.V2Transaction, fn func(i, j uint64, leaves []elementLeaf)) {
	clearBits := func(x uint64, n int) uint64 { return x &^ (1<<n - 1) }

	var trees [64][]elementLeaf
	forEachElementLeaf(txns, func(l elementLeaf) {
		trees[len(l.MerkleProof)] = append(trees[len(l.MerkleProof)], l)
	})
	for height, leaves := range &trees {
		if len(leaves) == 0 {
			continue
		}
		sort.Slice(leaves, func(i, j int) bool {
			return leaves[i].LeafIndex < leaves[j].LeafIndex
		})
		start := clearBits(leaves[0].LeafIndex, height+1)
		end := start + 1<<height
		fn(start, end, leaves)
	}
}

func multiproofSize(txns []types.V2Transaction) int {
	var proofSize func(i, j uint64, leaves []elementLeaf) int
	proofSize = func(i, j uint64, leaves []elementLeaf) int {
		height := bits.TrailingZeros64(j - i)
		if len(leaves) == 0 {
			return 1
		} else if height == 0 {
			return 0
		}
		mid := (i + j) / 2
		left, right := splitLeaves(leaves, mid)
		return proofSize(i, mid, left) + proofSize(mid, j, right)
	}

	size := 0
	forEachTree(txns, func(i, j uint64, leaves []elementLeaf) {
		size += proofSize(i, j, leaves)
	})
	return size
}

func expandMultiproof(txns []types.V2Transaction, proof []types.Hash256) {
	var visit func(i, j uint64, leaves []elementLeaf) types.Hash256
	visit = func(i, j uint64, leaves []elementLeaf) types.Hash256 {
		height := bits.TrailingZeros64(j - i)
		if len(leaves) == 0 {
			// no leaves in this subtree; must have a proof root
			h := proof[0]
			proof = proof[1:]
			return h
		} else if height == 0 {
			return leaves[0].hash()
		}
		mid := (i + j) / 2
		left, right := splitLeaves(leaves, mid)
		leftRoot := visit(i, mid, left)
		rightRoot := visit(mid, j, right)
		for i := range right {
			right[i].MerkleProof[height-1] = leftRoot
		}
		for i := range left {
			left[i].MerkleProof[height-1] = rightRoot
		}
		return types.HashBytes(append(append([]byte{0x01}, leftRoot[:]...), rightRoot[:]...))
	}

	forEachTree(txns, func(i, j uint64, leaves []elementLeaf) {
		_ = visit(i, j, leaves)
	})
}

type V2TransactionsMultiproof []types.V2Transaction

func (txns *V2TransactionsMultiproof) DecodeFrom(d *types.Decoder) {
	*txns = make(V2TransactionsMultiproof, d.ReadPrefix())
	for i := range *txns {
		(*txns)[i].DecodeFrom(d)
	}
	forEachElementLeaf(*txns, func(l elementLeaf) {
		l.MerkleProof = make([]types.Hash256, d.ReadUint8())
		if len(l.MerkleProof) >= 64 {
			d.SetErr(errors.New("invalid Merkle proof size"))
		}
	})
	if d.Err() != nil {
		return
	}
	multiproof := make([]types.Hash256, multiproofSize(*txns))
	for i := range multiproof {
		multiproof[i].DecodeFrom(d)
	}
	expandMultiproof(*txns, multiproof)
}

func testnetFixMultiproofs(dir string) {
	bdb, err := bolt.Open(filepath.Join(dir, "consensus.db"), 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer bdb.Close()
	var needUpdate bool
	bdb.Update(func(tx *bolt.Tx) error {
		needUpdate = tx.Bucket([]byte("multiproof-fix")) == nil
		return nil
	})
	if !needUpdate {
		return
	}

	fmt.Println("Fixing consensus.db multiproofs...")

	type supplementedBlock struct {
		Block      types.Block
		Supplement *consensus.V1BlockSupplement
	}

	decodeBlock := func(v []byte) (sb supplementedBlock, err error) {
		d := types.NewBufDecoder(v)
		if v := d.ReadUint8(); v != 2 {
			d.SetErr(fmt.Errorf("incompatible version (%d)", v))
		}
		(*types.V1Block)(&sb.Block).DecodeFrom(d)
		if d.ReadBool() {
			sb.Block.V2 = new(types.V2BlockData)
			sb.Block.V2.Height = d.ReadUint64()
			sb.Block.V2.Commitment.DecodeFrom(d)
			(*V2TransactionsMultiproof)(&sb.Block.V2.Transactions).DecodeFrom(d)
		}
		if d.ReadBool() {
			sb.Supplement = new(consensus.V1BlockSupplement)
			sb.Supplement.DecodeFrom(d)
		}
		err = d.Err()
		return
	}
	encodeBlock := func(sb supplementedBlock) []byte {
		var buf bytes.Buffer
		e := types.NewEncoder(&buf)
		e.WriteUint8(2)
		(types.V2Block)(sb.Block).EncodeTo(e)
		e.WriteBool(sb.Supplement != nil)
		if sb.Supplement != nil {
			sb.Supplement.EncodeTo(e)
		}
		e.Flush()
		return buf.Bytes()
	}

	err = bdb.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("Blocks"))
		var keys []string
		bucket.ForEach(func(k, v []byte) error {
			keys = append(keys, string(k))
			return nil
		})
		for _, k := range keys {
			fmt.Printf("\r%x...", k)
			b, err := decodeBlock(bucket.Get([]byte(k)))
			if err != nil {
				return err
			}
			if err := bucket.Put([]byte(k), encodeBlock(b)); err != nil {
				return err
			}
		}
		_, err := tx.CreateBucket([]byte("multiproof-fix"))
		return err
	})
	if err != nil {
		fmt.Println()
		log.Fatal(err)
	}
	fmt.Println("done.")
}
