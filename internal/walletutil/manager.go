package walletutil

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"go.sia.tech/coreutils/chain"
	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
)

var errNoWallet = errors.New("wallet does not exist")

// A ChainManager manages blockchain state.
type ChainManager interface {
	AddSubscriber(s chain.Subscriber, tip types.ChainIndex) error
	RemoveSubscriber(s chain.Subscriber)
	BestIndex(height uint64) (types.ChainIndex, bool)
}

type managedEphemeralWallet struct {
	w          *EphemeralStore
	info       json.RawMessage
	subscribed bool
}

// An EphemeralWalletManager manages multiple ephemeral wallet stores.
type EphemeralWalletManager struct {
	cm      ChainManager
	mu      sync.Mutex
	wallets map[string]*managedEphemeralWallet
}

// AddWallet implements api.WalletManager.
func (wm *EphemeralWalletManager) AddWallet(name string, info json.RawMessage) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if _, ok := wm.wallets[name]; ok {
		return errors.New("wallet already exists")
	}
	store := NewEphemeralStore()
	wm.wallets[name] = &managedEphemeralWallet{store, info, false}
	return nil
}

// DeleteWallet implements api.WalletManager.
func (wm *EphemeralWalletManager) DeleteWallet(name string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	delete(wm.wallets, name)
	return nil
}

// Wallets implements api.WalletManager.
func (wm *EphemeralWalletManager) Wallets() map[string]json.RawMessage {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	ws := make(map[string]json.RawMessage, len(wm.wallets))
	for name, w := range wm.wallets {
		ws[name] = w.info
	}
	return ws
}

// AddAddress implements api.WalletManager.
func (wm *EphemeralWalletManager) AddAddress(name string, addr types.Address, info json.RawMessage) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return errNoWallet
	}
	return mw.w.AddAddress(addr, info)
}

// RemoveAddress implements api.WalletManager.
func (wm *EphemeralWalletManager) RemoveAddress(name string, addr types.Address) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return errNoWallet
	}
	return mw.w.RemoveAddress(addr)
}

// Addresses implements api.WalletManager.
func (wm *EphemeralWalletManager) Addresses(name string) (map[types.Address]json.RawMessage, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return nil, errNoWallet
	}
	return mw.w.Addresses()
}

// Events implements api.WalletManager.
func (wm *EphemeralWalletManager) Events(name string, offset, limit int) ([]wallet.Event, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return nil, errNoWallet
	}
	return mw.w.Events(offset, limit)
}

// Annotate implements api.WalletManager.
func (wm *EphemeralWalletManager) Annotate(name string, txns []types.Transaction) ([]wallet.PoolTransaction, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return nil, errNoWallet
	}
	return mw.w.Annotate(txns), nil
}

// UnspentOutputs implements api.WalletManager.
func (wm *EphemeralWalletManager) UnspentOutputs(name string) ([]types.SiacoinElement, []types.SiafundElement, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return nil, nil, errNoWallet
	}
	return mw.w.UnspentOutputs()
}

// SubscribeWallet implements api.WalletManager.
func (wm *EphemeralWalletManager) SubscribeWallet(name string, startHeight uint64) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return errNoWallet
	} else if mw.subscribed {
		return errors.New("already subscribed")
	}
	// AddSubscriber applies each block *after* index, but we want to *include*
	// the block at startHeight, so subtract one.
	//
	// NOTE: if subscribing from height 0, we must pass an empty index in order
	// to receive the genesis block.
	var index types.ChainIndex
	if startHeight > 0 {
		if index, ok = wm.cm.BestIndex(startHeight - 1); !ok {
			return errors.New("invalid height")
		}
	}
	if err := wm.cm.AddSubscriber(mw.w, index); err != nil {
		return err
	}
	mw.subscribed = true
	return nil
}

// NewEphemeralWalletManager returns a new EphemeralWalletManager.
func NewEphemeralWalletManager(cm ChainManager) *EphemeralWalletManager {
	return &EphemeralWalletManager{
		cm:      cm,
		wallets: make(map[string]*managedEphemeralWallet),
	}
}

type managedJSONWallet struct {
	w          *JSONStore
	info       json.RawMessage
	subscribed bool
}

type managerPersistData struct {
	Wallets []managerPersistWallet `json:"wallets"`
}

type managerPersistWallet struct {
	Name       string          `json:"name"`
	Info       json.RawMessage `json:"info"`
	Subscribed bool            `json:"subscribed"`
}

// A JSONWalletManager manages multiple JSON wallet stores.
type JSONWalletManager struct {
	dir     string
	cm      ChainManager
	mu      sync.Mutex
	wallets map[string]*managedJSONWallet
}

func (wm *JSONWalletManager) save() error {
	var p managerPersistData
	for name, mw := range wm.wallets {
		p.Wallets = append(p.Wallets, managerPersistWallet{name, mw.info, mw.subscribed})
	}
	js, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	dst := filepath.Join(wm.dir, "wallets.json")
	f, err := os.OpenFile(dst+"_tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0660)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(js); err != nil {
		return err
	} else if f.Sync(); err != nil {
		return err
	} else if f.Close(); err != nil {
		return err
	} else if err := os.Rename(dst+"_tmp", dst); err != nil {
		return err
	}
	return nil
}

func (wm *JSONWalletManager) load() error {
	dst := filepath.Join(wm.dir, "wallets.json")
	f, err := os.Open(dst)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	defer f.Close()
	var p managerPersistData
	if err := json.NewDecoder(f).Decode(&p); err != nil {
		return err
	}
	for _, pw := range p.Wallets {
		wm.wallets[pw.Name] = &managedJSONWallet{nil, pw.Info, pw.Subscribed}
	}
	return nil
}

// AddWallet implements api.WalletManager.
func (wm *JSONWalletManager) AddWallet(name string, info json.RawMessage) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if mw, ok := wm.wallets[name]; ok {
		// update existing wallet
		mw.info = info
		return wm.save()
	} else if _, err := os.Stat(filepath.Join(wm.dir, "wallets", name+".json")); err == nil {
		// shouldn't happen in normal conditions
		return errors.New("a wallet with that name already exists, but is absent from wallets.json")
	}
	store, _, err := NewJSONStore(filepath.Join(wm.dir, "wallets", name+".json"))
	if err != nil {
		return err
	}
	wm.wallets[name] = &managedJSONWallet{store, info, false}
	return wm.save()
}

// DeleteWallet implements api.WalletManager.
func (wm *JSONWalletManager) DeleteWallet(name string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return nil
	}
	wm.cm.RemoveSubscriber(mw.w)
	delete(wm.wallets, name)
	return os.RemoveAll(filepath.Join(wm.dir, "wallets", name+".json"))
}

// Wallets implements api.WalletManager.
func (wm *JSONWalletManager) Wallets() map[string]json.RawMessage {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	ws := make(map[string]json.RawMessage, len(wm.wallets))
	for name, w := range wm.wallets {
		ws[name] = w.info
	}
	return ws
}

// AddAddress implements api.WalletManager.
func (wm *JSONWalletManager) AddAddress(name string, addr types.Address, info json.RawMessage) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return errNoWallet
	}
	return mw.w.AddAddress(addr, info)
}

// RemoveAddress implements api.WalletManager.
func (wm *JSONWalletManager) RemoveAddress(name string, addr types.Address) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return errNoWallet
	}
	return mw.w.RemoveAddress(addr)
}

// Addresses implements api.WalletManager.
func (wm *JSONWalletManager) Addresses(name string) (map[types.Address]json.RawMessage, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return nil, errNoWallet
	}
	return mw.w.Addresses()
}

// Events implements api.WalletManager.
func (wm *JSONWalletManager) Events(name string, offset, limit int) ([]wallet.Event, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return nil, errNoWallet
	}
	return mw.w.Events(offset, limit)
}

// Annotate implements api.WalletManager.
func (wm *JSONWalletManager) Annotate(name string, txns []types.Transaction) ([]wallet.PoolTransaction, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return nil, errNoWallet
	}
	return mw.w.Annotate(txns), nil
}

// UnspentOutputs implements api.WalletManager.
func (wm *JSONWalletManager) UnspentOutputs(name string) ([]types.SiacoinElement, []types.SiafundElement, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return nil, nil, errNoWallet
	}
	return mw.w.UnspentOutputs()
}

// SubscribeWallet implements api.WalletManager.
func (wm *JSONWalletManager) SubscribeWallet(name string, startHeight uint64) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return errNoWallet
	} else if mw.subscribed {
		return errors.New("already subscribed")
	}
	// AddSubscriber applies each block *after* index, but we want to *include*
	// the block at startHeight, so subtract one.
	//
	// NOTE: if subscribing from height 0, we must pass an empty index in order
	// to receive the genesis block.
	var index types.ChainIndex
	if startHeight > 0 {
		if index, ok = wm.cm.BestIndex(startHeight - 1); !ok {
			return errors.New("invalid height")
		}
	}
	if err := wm.cm.AddSubscriber(mw.w, index); err != nil {
		return err
	}
	mw.subscribed = true
	return wm.save()
}

// NewJSONWalletManager returns a wallet manager that stores wallets in the
// specified directory.
func NewJSONWalletManager(dir string, cm ChainManager) (*JSONWalletManager, error) {
	wm := &JSONWalletManager{
		dir:     dir,
		cm:      cm,
		wallets: make(map[string]*managedJSONWallet),
	}
	if err := os.MkdirAll(filepath.Join(dir, "wallets"), 0700); err != nil {
		return nil, err
	} else if err := wm.load(); err != nil {
		return nil, err
	}
	for name, mw := range wm.wallets {
		store, tip, err := NewJSONStore(filepath.Join(dir, "wallets", name+".json"))
		if err != nil {
			return nil, err
		}
		if mw.subscribed {
			if err := cm.AddSubscriber(store, tip); err != nil {
				return nil, err
			}
		}
		mw.w = store
	}
	return wm, nil
}
