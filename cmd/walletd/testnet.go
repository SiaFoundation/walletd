package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"time"

	bolt "go.etcd.io/bbolt"
	"go.sia.tech/core/chain"
	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/walletd/api"
	"go.sia.tech/walletd/wallet"
	"golang.org/x/term"
	"lukechampine.com/frand"
)

// TestnetAnagami returns the chain parameters and genesis block for the "Anagami"
// testnet chain.
func TestnetAnagami() (*consensus.Network, types.Block) {
	n := &consensus.Network{
		Name: "anagami",

		InitialCoinbase: types.Siacoins(300000),
		MinimumCoinbase: types.Siacoins(300000),
		InitialTarget:   types.BlockID{3: 1},
	}

	n.HardforkDevAddr.Height = 1
	n.HardforkDevAddr.OldAddress = types.Address{}
	n.HardforkDevAddr.NewAddress = types.Address{}

	n.HardforkTax.Height = 2

	n.HardforkStorageProof.Height = 3

	n.HardforkOak.Height = 5
	n.HardforkOak.FixHeight = 8
	n.HardforkOak.GenesisTimestamp = time.Unix(1702300000, 0) // Dec 11, 2023 @ 13:06 GMT

	n.HardforkASIC.Height = 13
	n.HardforkASIC.OakTime = 10 * time.Minute
	n.HardforkASIC.OakTarget = n.InitialTarget

	n.HardforkFoundation.Height = 21
	n.HardforkFoundation.PrimaryAddress, _ = types.ParseAddress("addr:5949fdf56a7c18ba27f6526f22fd560526ce02a1bd4fa3104938ab744b69cf63b6b734b8341f")
	n.HardforkFoundation.FailsafeAddress = n.HardforkFoundation.PrimaryAddress

	n.HardforkV2.AllowHeight = 2016         // ~2 weeks in
	n.HardforkV2.RequireHeight = 2016 + 288 // ~2 days later

	b := types.Block{
		Timestamp: n.HardforkOak.GenesisTimestamp,
		Transactions: []types.Transaction{{
			SiacoinOutputs: []types.SiacoinOutput{{
				Address: n.HardforkFoundation.PrimaryAddress,
				Value:   types.Siacoins(1).Mul64(1e12),
			}},
			SiafundOutputs: []types.SiafundOutput{{
				Address: n.HardforkFoundation.PrimaryAddress,
				Value:   10000,
			}},
		}},
	}

	return n, b
}

func loadTestnetSeed(s string) wallet.Seed {
	if s == "" {
		fmt.Println("Seed not supplied via -seed flag, falling back to manual entry.")
		fmt.Print("Seed: ")
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		check("Could not read API password:", err)
		if err != nil {
			log.Fatal(err)
		}
		s = string(pw)
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 8 {
		log.Fatal("Seed must be 16 hex characters")
	}
	var entropy [32]byte
	copy(entropy[:], b)
	return wallet.NewSeedFromEntropy(&entropy)
}

func initTestnetClient(addr string, network string, seed wallet.Seed) *api.Client {
	if network == "mainnet" {
		log.Fatal("Testnet actions cannot be used on mainnet")
	}
	c := api.NewClient("http://"+addr+"/api", getAPIPassword())
	cs, err := c.ConsensusTipState()
	check("Couldn't connect to API:", err)
	if cs.Network.Name != network {
		log.Fatalf("Testnet %q was specified, but walletd is running %v", network, cs.Network.Name)
	}
	ourAddr := types.StandardUnlockHash(seed.PublicKey(0))
	wc := c.Wallet("primary")
	if addrs, err := wc.Addresses(); err == nil && len(addrs) > 0 {
		if _, ok := addrs[ourAddr]; !ok {
			log.Fatal("Wallet already initialized with a different testnet address")
		}
	}
	if ws, _ := c.Wallets(); len(ws) == 0 {
		fmt.Print("Initializing testnet wallet...")
		c.AddWallet("primary", nil)
		if err := wc.AddAddress(ourAddr, nil); err != nil {
			fmt.Println()
			log.Fatal(err)
		} else if err := wc.Subscribe(0); err != nil {
			fmt.Println()
			log.Fatal(err)
		}
		fmt.Println("done.")
	}
	return c
}

func mineBlock(cs consensus.State, b *types.Block) (hashes int, found bool) {
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
	startBlock := time.Now()
	for types.BlockID(types.HashBytes(buf)).CmpWork(cs.ChildTarget) < 0 {
		b.Nonce += factor
		hashes++
		binary.LittleEndian.PutUint64(buf[32:], b.Nonce)
		if time.Since(startBlock) > 10*time.Second {
			return hashes, false
		}
	}
	return hashes, true
}

func runTestnetMiner(c *api.Client, seed wallet.Seed) {
	minerAddr := types.StandardUnlockHash(seed.PublicKey(0))
	log.Println("Started mining into", minerAddr)
	start := time.Now()

	var hashes float64
	var blocks uint64
outer:
	for {
		elapsed := time.Since(start)
		cs, err := c.ConsensusTipState()
		check("Couldn't get consensus tip state:", err)
		n := big.NewInt(int64(hashes))
		n.Mul(n, big.NewInt(int64(24*time.Hour)))
		d, _ := new(big.Int).SetString(cs.Difficulty.String(), 10)
		d.Mul(d, big.NewInt(int64(1+elapsed)))
		r, _ := new(big.Rat).SetFrac(n, d).Float64()
		log.Printf("Mining...(%.2f kH/s, %.2f blocks/day (expected: %.2f), difficulty %v)", hashes/elapsed.Seconds()/1000, float64(blocks)*float64(24*time.Hour)/float64(elapsed), r, cs.Difficulty)

		txns, v2txns, err := c.TxpoolTransactions()
		check("Couldn't get txpool transactions:", err)
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
		h, ok := mineBlock(cs, &b)
		hashes += float64(h)
		if !ok {
			continue outer
		}
		blocks++
		index := types.ChainIndex{Height: cs.Index.Height + 1, ID: b.ID()}
		tip, err := c.ConsensusTip()
		check("Couldn't get consensus tip:", err)
		if tip != cs.Index {
			log.Printf("Mined %v but tip changed, starting over", index)
		} else if err := c.SyncerBroadcastBlock(b); err != nil {
			log.Println("Mined invalid block:", err)
		} else if b.V2 == nil {
			log.Printf("Found v1 block %v", index)
		} else {
			log.Printf("Found v2 block %v", index)
		}
	}
}

func sendTestnet(c *api.Client, seed wallet.Seed, amount types.Currency, dest types.Address, v2 bool) {
	ourKey := seed.PrivateKey(0)
	ourUC := types.StandardUnlockConditions(seed.PublicKey(0))
	ourAddr := types.StandardUnlockHash(seed.PublicKey(0))

	cs, err := c.ConsensusTipState()
	check("Couldn't get consensus tip state:", err)
	utxos, _, err := c.Wallet("primary").Outputs()
	check("Couldn't get outputs:", err)
	txns, v2txns, err := c.TxpoolTransactions()
	if err != nil {
		log.Fatal(err)
	}
	inPool := make(map[types.Hash256]bool)
	for _, ptxn := range txns {
		for _, in := range ptxn.SiacoinInputs {
			inPool[types.Hash256(in.ParentID)] = true
		}
	}
	for _, ptxn := range v2txns {
		for _, in := range ptxn.SiacoinInputs {
			inPool[in.Parent.ID] = true
		}
	}

	frand.Shuffle(len(utxos), reflect.Swapper(utxos))
	var inputSum types.Currency
	rem := utxos[:0]
	for _, utxo := range utxos {
		if inputSum.Cmp(amount) >= 0 {
			break
		} else if cs.Index.Height > utxo.MaturityHeight && !inPool[utxo.ID] {
			rem = append(rem, utxo)
			inputSum = inputSum.Add(utxo.SiacoinOutput.Value)
		}
	}
	utxos = rem
	if inputSum.Cmp(amount) < 0 {
		log.Fatal("Insufficient balance")
	}
	outputs := []types.SiacoinOutput{
		{Address: dest, Value: amount},
	}
	minerFee := inputSum.Sub(amount)
	if maxFee := types.Siacoins(1); minerFee.Cmp(maxFee) > 0 {
		minerFee = maxFee
	}
	if change := inputSum.Sub(amount.Add(minerFee)); !change.IsZero() {
		outputs = append(outputs, types.SiacoinOutput{
			Address: ourAddr,
			Value:   change,
		})
	}

	if v2 {
		txn := types.V2Transaction{
			SiacoinInputs:  make([]types.V2SiacoinInput, len(utxos)),
			SiacoinOutputs: outputs,
			MinerFee:       minerFee,
		}
		for i, sce := range utxos {
			txn.SiacoinInputs[i].Parent = sce
			txn.SiacoinInputs[i].SatisfiedPolicy.Policy = types.SpendPolicy{
				Type: types.PolicyTypeUnlockConditions(ourUC),
			}
		}
		sigHash := cs.InputSigHash(txn)
		for i := range utxos {
			txn.SiacoinInputs[i].SatisfiedPolicy.Signatures = []types.Signature{ourKey.SignHash(sigHash)}
		}
		if err := c.TxpoolBroadcast(nil, []types.V2Transaction{txn}); err != nil {
			log.Fatal(err)
		}
		log.Println("Broadcast", txn.ID(), "successfully")
	} else {
		txn := types.Transaction{
			SiacoinInputs:  make([]types.SiacoinInput, len(utxos)),
			SiacoinOutputs: outputs,
			Signatures:     make([]types.TransactionSignature, len(utxos)),
		}
		if !minerFee.IsZero() {
			txn.MinerFees = append(txn.MinerFees, minerFee)
		}
		for i, sce := range utxos {
			txn.SiacoinInputs[i] = types.SiacoinInput{
				ParentID:         types.SiacoinOutputID(sce.ID),
				UnlockConditions: ourUC,
			}
		}
		cs, _ := c.ConsensusTipState()
		for i, sce := range utxos {
			txn.Signatures[i] = wallet.StandardTransactionSignature(sce.ID)
			wallet.SignTransaction(cs, &txn, i, ourKey)
		}
		if err := c.TxpoolBroadcast([]types.Transaction{txn}, nil); err != nil {
			log.Fatal(err)
		}
		log.Println("Broadcast", txn.ID(), "successfully")
	}
}

func printTestnetEvents(c *api.Client, seed wallet.Seed) {
	ourAddr := types.StandardUnlockHash(seed.PublicKey(0))
	events, err := c.Wallet("primary").Events(0, -1)
	check("Couldn't get events:", err)
	for i := range events {
		e := events[len(events)-1-i]
		switch t := e.Val.(type) {
		case *wallet.EventTransaction:
			if len(t.SiacoinInputs) == 0 || len(t.SiacoinOutputs) == 0 {
				continue
			}
			sci := t.SiacoinInputs[0].SiacoinOutput
			sco := t.SiacoinOutputs[0].SiacoinOutput
			if sci.Address == ourAddr {
				fmt.Printf("%14v (%v): Sent %v (+ %v fee) to %v\n", e.Index, e.Timestamp.Format("Jan _2 @ 15:04:05"), sco.Value, t.Fee, sco.Address)
			} else {
				fmt.Printf("%14v (%v): Received %v from %v\n", e.Index, e.Timestamp.Format("Jan _2 @ 15:04:05"), sco.Value, sci.Address)
			}
		case *wallet.EventMinerPayout:
			sco := t.SiacoinOutput.SiacoinOutput
			fmt.Printf("%14v (%v): Earned %v miner payout from block %v\n", e.Index, e.Timestamp.Format("Jan _2 @ 15:04:05"), sco.Value, e.Index)
		}
	}
}

func testnetTxpoolBalance(c *api.Client, seed wallet.Seed) (gained, lost types.Currency) {
	ourAddr := types.StandardUnlockHash(seed.PublicKey(0))
	txns, v2txns, err := c.TxpoolTransactions()
	check("Couldn't get txpool transactions:", err)
	for _, txn := range txns {
		if len(txn.SiacoinInputs) == 0 || len(txn.SiacoinOutputs) == 0 {
			continue
		}
		sco := txn.SiacoinOutputs[0]
		if txn.SiacoinInputs[0].UnlockConditions.UnlockHash() == ourAddr {
			lost = lost.Add(sco.Value).Add(txn.TotalFees())
		} else if sco.Address == ourAddr {
			gained = gained.Add(sco.Value)
		}
	}
	for _, txn := range v2txns {
		if len(txn.SiacoinInputs) == 0 || len(txn.SiacoinOutputs) == 0 {
			continue
		}
		sco := txn.SiacoinOutputs[0]
		if txn.SiacoinInputs[0].Parent.SiacoinOutput.Address == ourAddr {
			lost = lost.Add(sco.Value).Add(txn.MinerFee)
		} else if sco.Address == ourAddr {
			gained = gained.Add(sco.Value)
		}
	}
	return
}

func printTestnetTxpool(c *api.Client, seed wallet.Seed) {
	ourAddr := types.StandardUnlockHash(seed.PublicKey(0))
	txns, v2txns, err := c.TxpoolTransactions()
	check("Couldn't get txpool transactions:", err)
	if len(txns) == 0 && len(v2txns) == 0 {
		fmt.Println("No transactions in txpool.")
		return
	}
	for _, txn := range txns {
		if len(txn.SiacoinInputs) == 0 || len(txn.SiacoinOutputs) == 0 {
			continue
		}
		id := txn.ID()
		sci := txn.SiacoinInputs[0]
		sco := txn.SiacoinOutputs[0]
		if sci.UnlockConditions.UnlockHash() == ourAddr {
			fmt.Printf("%x (v1): Sending %v (+ %v fee) to %v\n", id[:4], sco.Value, txn.TotalFees(), sco.Address)
		} else if sco.Address == ourAddr {
			fmt.Printf("%x (v1): Receiving %v from %v\n", id[:4], sco.Value, sci.UnlockConditions.UnlockHash())
		}
	}
	for _, txn := range v2txns {
		if len(txn.SiacoinInputs) == 0 || len(txn.SiacoinOutputs) == 0 {
			continue
		}
		id := txn.ID()
		sci := txn.SiacoinInputs[0].Parent.SiacoinOutput
		sco := txn.SiacoinOutputs[0]
		if sci.Address == ourAddr {
			fmt.Printf("%x (v2): Sending %v (+ %v fee) to %v\n", id[:4], sco.Value, txn.MinerFee, sco.Address)
		} else if sco.Address == ourAddr {
			fmt.Printf("%x (v2): Receiving %v from %v\n", id[:4], sco.Value, sci.Address)
		}
	}
}

func testnetFixDBTree(dir string) {
	bdb, err := bolt.Open(filepath.Join(dir, "consensus.db"), 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	db := &boltDB{db: bdb}
	defer db.Close()
	if db.Bucket([]byte("tree-fix-2")) != nil {
		return
	}

	fmt.Print("Fixing consensus.db Merkle tree...")

	network, genesisBlock := TestnetAnagami()
	dbstore, tipState, err := chain.NewDBStore(db, network, genesisBlock)
	if err != nil {
		log.Fatal(err)
	}
	cm := chain.NewManager(dbstore, tipState)

	bdb2, err := bolt.Open(filepath.Join(dir, "consensus.db-fixed"), 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	db2 := &boltDB{db: bdb2}
	defer db2.Close()
	dbstore2, tipState2, err := chain.NewDBStore(db2, network, genesisBlock)
	if err != nil {
		log.Fatal(err)
	}
	cm2 := chain.NewManager(dbstore2, tipState2)

	for cm2.Tip() != cm.Tip() {
		fmt.Printf("\rFixing consensus.db Merkle tree...%v/%v", cm2.Tip().Height, cm.Tip().Height)
		index, _ := cm.BestIndex(cm2.Tip().Height + 1)
		b, _ := cm.Block(index.ID)
		if err := cm2.AddBlocks([]types.Block{b}); err != nil {
			break
		}
	}
	fmt.Println()

	if _, err := db2.CreateBucket([]byte("tree-fix-2")); err != nil {
		log.Fatal(err)
	} else if err := db.Close(); err != nil {
		log.Fatal(err)
	} else if err := db2.Close(); err != nil {
		log.Fatal(err)
	} else if err := os.Rename(filepath.Join(dir, "consensus.db-fixed"), filepath.Join(dir, "consensus.db")); err != nil {
		log.Fatal(err)
	}

	fmt.Print("Backing up old wallet state...")
	os.Rename(filepath.Join(dir, "wallets.json"), filepath.Join(dir, "wallets.json-bck"))
	os.Rename(filepath.Join(dir, "wallets"), filepath.Join(dir, "wallets-bck"))
	fmt.Println("done.")
	fmt.Println("NOTE: Your wallet will resync automatically on first use; this may take a few seconds.")
}
