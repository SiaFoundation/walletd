package main

import (
	"log"
	"os"
	"path/filepath"

	"go.sia.tech/siad/modules"
	"go.sia.tech/siad/modules/consensus"
	"go.sia.tech/siad/modules/gateway"
	"go.sia.tech/siad/modules/transactionpool"
	"go.sia.tech/walletd/internal/walletutil"
	"go.sia.tech/walletd/wallet"
)

type node struct {
	g  modules.Gateway
	cm modules.ConsensusSet
	tp modules.TransactionPool
	w  *wallet.HotWallet
}

func (n *node) Close() error {
	errs := []error{
		n.g.Close(),
		n.cm.Close(),
		n.tp.Close(),
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func newNode(addr, dir string) (*node, error) {
	gatewayDir := filepath.Join(dir, "gateway")
	if err := os.MkdirAll(gatewayDir, 0700); err != nil {
		return nil, err
	}
	g, err := gateway.New(addr, false, gatewayDir)
	if err != nil {
		return nil, err
	}
	consensusDir := filepath.Join(dir, "consensus")
	if err := os.MkdirAll(consensusDir, 0700); err != nil {
		return nil, err
	}
	cm, errCh := consensus.New(g, false, consensusDir)
	select {
	case err := <-errCh:
		return nil, err
	default:
		go func() {
			if err := <-errCh; err != nil {
				log.Println("WARNING: consensus initialization returned an error:", err)
			}
		}()
	}
	tpoolDir := filepath.Join(dir, "transactionpool")
	if err := os.MkdirAll(tpoolDir, 0700); err != nil {
		return nil, err
	}
	tp, err := transactionpool.New(cm, g, tpoolDir)
	if err != nil {
		return nil, err
	}

	// TODO: persist
	store := walletutil.NewEphemeralStore()
	w := wallet.NewHotWallet(store, wallet.NewSeed())

	ccid, err := store.ConsensusChangeID()
	if err != nil {
		return nil, err
	}
	if err := cm.ConsensusSetSubscribe(store, ccid, nil); err != nil {
		return nil, err
	}

	return &node{
		g:  g,
		cm: cm,
		tp: tp,
		w:  w,
	}, nil
}
