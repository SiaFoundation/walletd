package main

import (
	"log"
	"net"

	bolt "go.etcd.io/bbolt"
	"go.sia.tech/core/chain"
	"go.sia.tech/core/gateway"
	"go.sia.tech/walletd/internal/walletutil"
	"go.sia.tech/walletd/syncer"
	"go.sia.tech/walletd/txpool"
)

type boltDB struct {
	db *bolt.DB
}

func (db boltDB) View(fn func(chain.DBTx) error) error {
	return db.db.View(func(tx *bolt.Tx) error {
		return fn(boltTx{tx})
	})
}

func (db boltDB) Update(fn func(chain.DBTx) error) error {
	return db.db.Update(func(tx *bolt.Tx) error {
		return fn(boltTx{tx})
	})
}

type boltTx struct {
	tx *bolt.Tx
}

func (tx boltTx) Bucket(name []byte) chain.DBBucket {
	b := tx.tx.Bucket(name)
	if b == nil {
		return nil
	}
	return b
}

func (tx boltTx) CreateBucket(name []byte) (chain.DBBucket, error) {
	b, err := tx.tx.CreateBucket(name)
	if b == nil {
		return nil, err
	}
	return b, nil
}

func (tx boltTx) DeleteBucket(name []byte) error {
	return tx.tx.DeleteBucket(name)
}

type node struct {
	cm *chain.Manager
	tp *txpool.TxPool
	s  *syncer.Syncer
	w  *walletutil.JSONStore

	Close func() error
}

func newNode(addr, dir string) (*node, error) {
	bdb, err := bolt.Open("consensus.db", 0600, nil)
	if err != nil {
		log.Fatal(err)
	}
	network, genesisBlock := chain.Mainnet()
	dbstore, tip, err := chain.NewDBStore(boltDB{bdb}, network, genesisBlock)
	if err != nil {
		return nil, err
	}
	cm := chain.NewManager(dbstore, tip.State)

	tp := txpool.New(nil)

	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	header := gateway.Header{
		GenesisID:  genesisBlock.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: addr,
	}
	bootstrapPeers := []string{
		"144.217.7.188:9981",
		"5.19.177.22:9981",
		"176.104.8.173:9981",
	}
	pm, err := syncer.NewJSONPeerManager("peers.json", bootstrapPeers)
	if err != nil {
		log.Fatal(err)
	}
	s := syncer.New(l, cm, tp, pm, header, syncer.WithLogger((*syncer.StdLogger)(log.Default())))

	w, wtip, err := walletutil.NewJSONStore(dir)
	if err != nil {
		return nil, err
	} else if err := cm.AddSubscriber(w, wtip); err != nil {
		return nil, err
	}

	return &node{
		cm: cm,
		tp: tp,
		s:  s,
		w:  w,
		Close: func() error {
			errs := []error{
				s.Close(), // closes l
				bdb.Close(),
			}
			for _, err := range errs {
				if err != nil {
					return err
				}
			}
			return nil
		},
	}, nil
}
