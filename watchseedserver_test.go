package walrus

import (
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
	"lukechampine.com/us/wallet"
)

func runWatchSeedServer(h http.Handler) (*WatchSeedClient, func() error) {
	l, err := net.Listen("tcp", "localhost:9990")
	if err != nil {
		panic(err)
	}
	srv := http.Server{Handler: h}
	go srv.Serve(l)
	return NewWatchSeedClient(l.Addr().String()), srv.Close
}

func TestWatchSeedServer(t *testing.T) {
	dir, err := ioutil.TempDir("", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	store, err := wallet.NewBoltDBStore(filepath.Join(dir, "wallet.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	defer os.RemoveAll(dir)

	w := wallet.NewWatchOnlyWallet(store)
	cs := new(mockCS)
	cs.ConsensusSetSubscribe(w, store.ConsensusChangeID(), nil)
	ss := NewWatchSeedServer(w, stubTpool{})
	client, stop := runWatchSeedServer(ss)
	defer stop()

	// initial balance should be zero
	if balance, err := client.Balance(); err != nil {
		t.Fatal(err)
	} else if !balance.IsZero() {
		t.Fatal("balance should be zero")
	}

	// shouldn't have any transactions yet
	if txnHistory, err := client.Transactions(-1); err != nil {
		t.Fatal(err)
	} else if len(txnHistory) != 0 {
		t.Fatal("transaction history should be empty")
	}

	// shouldn't have any addresses yet
	if addresses, err := client.Addresses(); err != nil {
		t.Fatal(err)
	} else if len(addresses) != 0 {
		t.Fatal("address list should be empty")
	}

	// create and add an address
	seed := wallet.NewSeed()
	addrInfo := wallet.SeedAddressInfo{
		UnlockConditions: wallet.StandardUnlockConditions(seed.PublicKey(0)),
		KeyIndex:         0,
	}
	addr := addrInfo.UnlockConditions.UnlockHash()
	if err := client.WatchAddress(addrInfo); err != nil {
		t.Fatal(err)
	}

	// should have an address now
	if addresses, err := client.Addresses(); err != nil {
		t.Fatal(err)
	} else if len(addresses) != 1 || addresses[0] != addr {
		t.Fatal("bad address list", addresses)
	}

	// address info should be present
	if addrInfo, err := client.AddressInfo(addr); err != nil {
		t.Fatal(err)
	} else if addrInfo.KeyIndex != 0 || addrInfo.UnlockConditions.UnlockHash() != addr {
		t.Fatal("address info is inaccurate")
	}

	// simulate a transaction
	cs.sendTxn(types.Transaction{
		SiacoinOutputs: []types.SiacoinOutput{
			{UnlockHash: addr, Value: types.SiacoinPrecision.Div64(2)},
			{UnlockHash: addr, Value: types.SiacoinPrecision.Div64(2)},
		},
	})

	// get new balance
	if balance, err := client.Balance(); err != nil {
		t.Fatal(err)
	} else if balance.Cmp(types.SiacoinPrecision) != 0 {
		t.Fatal("balance should be 1 SC")
	}

	// transaction should appear in history
	txnHistory, err := client.Transactions(2)
	if err != nil {
		t.Fatal(err)
	} else if len(txnHistory) != 1 {
		t.Fatal("transaction should appear in history")
	}
	if htx, err := client.Transaction(txnHistory[0]); err != nil {
		t.Fatal(err)
	} else if len(htx.Transaction.SiacoinOutputs) != 2 {
		t.Fatal("transaction should have two outputs")
	}

	// create an unsigned transaction using available outputs
	outputs, err := client.UnspentOutputs()
	if err != nil {
		t.Fatal(err)
	} else if len(outputs) != 2 {
		t.Fatal("should have two UTXOs")
	}

	inputs := make([]wallet.ValuedInput, len(outputs))
	for i, o := range outputs {
		inputs[i] = wallet.ValuedInput{
			SiacoinInput: types.SiacoinInput{
				ParentID:         o.ID,
				UnlockConditions: o.UnlockConditions,
			},
			Value: o.Value,
		}
	}
	amount := types.SiacoinPrecision.Div64(2)
	dest := types.UnlockHash{}
	fee := types.NewCurrency64(10)
	txn, ok := sendSiacoins(amount, dest, fee, inputs, addr)
	if !ok {
		t.Fatal("insufficient funds")
	}

	// sign and broadcast the transaction
	for _, sci := range txn.SiacoinInputs {
		txnSig := wallet.StandardTransactionSignature(crypto.Hash(sci.ParentID))
		wallet.AppendTransactionSignature(&txn, txnSig, seed.SecretKey(0))
	}
	if err := txn.StandaloneValid(types.ASICHardforkHeight + 1); err != nil {
		t.Fatal(err)
	} else if err := client.Broadcast([]types.Transaction{txn}); err != nil {
		t.Fatal(err)
	}

	// outputs should no longer be reported as spendable
	if outputs, err := client.UnspentOutputs(); err != nil {
		t.Fatal(err)
	} else if len(outputs) != 0 {
		t.Fatal("should have zero UTXOs")
	}

	// instead, they should appear in limbo
	limbo, err := client.LimboOutputs()
	if err != nil {
		t.Fatal(err)
	} else if len(limbo) != 2 {
		t.Fatal("should have two UTXOs in limbo")
	}

	// bring back an output from limbo
	if err := client.RemoveFromLimbo(limbo[0].ID); err != nil {
		t.Fatal(err)
	}
	if outputs, err := client.UnspentOutputs(); err != nil {
		t.Fatal(err)
	} else if len(outputs) != 1 {
		t.Fatal("should have one UTXO")
	}
	if limbo, err := client.LimboOutputs(); err != nil {
		t.Fatal(err)
	} else if len(limbo) != 1 {
		t.Fatal("should have one UTXO in limbo")
	}
}

func TestWatchServerThreadSafety(t *testing.T) {
	store := wallet.NewEphemeralWatchOnlyStore()
	w := wallet.NewWatchOnlyWallet(store)
	cs := new(mockCS)
	cs.ConsensusSetSubscribe(w, store.ConsensusChangeID(), nil)
	ss := NewWatchSeedServer(w, stubTpool{})
	client, stop := runWatchSeedServer(ss)
	defer stop()

	randomAddr := func() (info wallet.SeedAddressInfo) {
		info.UnlockConditions = wallet.StandardUnlockConditions(wallet.NewSeed().PublicKey(0))
		return
	}
	addr := randomAddr().UnlockConditions.UnlockHash()
	w.AddAddress(addr, nil)

	txn := types.Transaction{
		SiacoinOutputs: []types.SiacoinOutput{
			{UnlockHash: addr, Value: types.SiacoinPrecision.Div64(2)},
		},
	}

	// create a bunch of goroutines that call routes and add transactions
	// concurrently
	funcs := []func(){
		func() { cs.sendTxn(txn) },
		func() { client.Balance() },
		func() { client.WatchAddress(randomAddr()) },
		func() { client.UnwatchAddress(randomAddr().UnlockConditions.UnlockHash()) },
		func() { client.Addresses() },
		func() { client.TransactionsByAddress(addr, 2) },
	}
	var wg sync.WaitGroup
	wg.Add(len(funcs))
	for _, fn := range funcs {
		go func(fn func()) {
			for i := 0; i < 10; i++ {
				time.Sleep(time.Duration(fastrand.Intn(10)) * time.Millisecond)
				fn()
			}
			wg.Done()
		}(fn)
	}
	wg.Wait()
}
