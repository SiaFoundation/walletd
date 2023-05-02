package walletutil

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.sia.tech/core/chain"
	"go.sia.tech/core/types"
	"go.sia.tech/walletd/wallet"
)

var errNoWallet = errors.New("wallet does not exist")

type ChainManager interface {
	AddSubscriber(s chain.Subscriber, tip types.ChainIndex) error
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
func (wm *EphemeralWalletManager) Events(name string, since time.Time, max int) ([]wallet.Event, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return nil, errNoWallet
	}
	return mw.w.Events(since, max)
}

// UnspentOutputs implements api.WalletManager.
func (wm *EphemeralWalletManager) UnspentOutputs(name string) ([]wallet.SiacoinElement, []wallet.SiafundElement, error) {
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
	} else if index, ok := wm.cm.BestIndex(startHeight); !ok {
		return errors.New("invalid height")
	} else if err := wm.cm.AddSubscriber(mw.w, index); err != nil {
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
	if _, ok := wm.wallets[name]; ok {
		return errors.New("wallet already exists")
	}
	store, _, err := NewJSONStore(filepath.Join(wm.dir, name+".json"))
	if err != nil {
		return err
	}
	wm.wallets[name] = &managedJSONWallet{store, info, false}
	return wm.save()
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
func (wm *JSONWalletManager) Events(name string, since time.Time, max int) ([]wallet.Event, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	mw, ok := wm.wallets[name]
	if !ok {
		return nil, errNoWallet
	}
	return mw.w.Events(since, max)
}

// UnspentOutputs implements api.WalletManager.
func (wm *JSONWalletManager) UnspentOutputs(name string) ([]wallet.SiacoinElement, []wallet.SiafundElement, error) {
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
	index, ok := wm.cm.BestIndex(startHeight)
	if !ok {
		return errors.New("invalid height")
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
		} else if err := cm.AddSubscriber(store, tip); err != nil {
			return nil, err
		}
		mw.w = store
	}
	return wm, nil
}
