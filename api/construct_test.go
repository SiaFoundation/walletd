package api_test

import (
	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/api"
	"go.sia.tech/walletd/v2/wallet"
)

func ExampleWalletClient_Construct() {
	const (
		apiAddress  = "localhost:9980/api"
		apiPassword = "password"
	)

	client := api.NewClient(apiAddress, apiPassword)

	// generate a recovery phrase
	phrase := wallet.NewSeedPhrase()

	// derive an address from the recovery phrase
	var seed [32]byte
	defer clear(seed[:])
	if err := wallet.SeedFromPhrase(&seed, phrase); err != nil {
		panic(err)
	}

	privateKey := wallet.KeyFromSeed(&seed, 0)
	spendPolicy := types.SpendPolicy{
		Type: types.PolicyTypeUnlockConditions{
			PublicKeys: []types.UnlockKey{
				privateKey.PublicKey().UnlockKey(),
			},
			SignaturesRequired: 1,
		},
	}
	address := spendPolicy.Address()

	// add a wallet
	w1, err := client.AddWallet(api.WalletUpdateRequest{
		Name:        "test",
		Description: "test wallet",
	})
	if err != nil {
		panic(err)
	}

	// init the wallet client to interact with the wallet
	wc := client.Wallet(w1.ID)

	err = wc.AddAddress(wallet.Address{
		Address:     address,
		SpendPolicy: &spendPolicy,
	})
	if err != nil {
		panic(err)
	}

	// create a transaction
	resp, err := wc.Construct([]types.SiacoinOutput{
		{Address: types.VoidAddress, Value: types.Siacoins(1)},
	}, nil, address)
	if err != nil {
		panic(err)
	}
	txn := resp.Transaction

	// sign the transaction
	cs, err := client.ConsensusTipState()
	if err != nil {
		panic(err)
	}

	for i, sig := range txn.Signatures {
		sigHash := cs.WholeSigHash(txn, sig.ParentID, 0, 0, nil)
		sig := privateKey.SignHash(sigHash)
		txn.Signatures[i].Signature = sig[:]
	}

	// broadcast the transaction
	if err := client.TxpoolBroadcast(resp.Basis, []types.Transaction{txn}, nil); err != nil {
		panic(err)
	}
}
