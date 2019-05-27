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
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/fastrand"
	"lukechampine.com/us/wallet"
)

type stubTpool struct{}

func (stubTpool) AcceptTransactionSet([]types.Transaction) (err error)   { return }
func (stubTpool) FeeEstimation() (min, max types.Currency)               { return }
func (stubTpool) TransactionSet(id crypto.Hash) (ts []types.Transaction) { return }

type mockCS struct {
	subscriber modules.ConsensusSetSubscriber
}

func (m *mockCS) ConsensusSetSubscribe(s modules.ConsensusSetSubscriber, ccid modules.ConsensusChangeID, cancel <-chan struct{}) error {
	m.subscriber = s
	return nil
}

func (m *mockCS) sendTxn(txn types.Transaction) {
	outputs := make([]modules.SiacoinOutputDiff, len(txn.SiacoinOutputs))
	for i := range outputs {
		outputs[i] = modules.SiacoinOutputDiff{
			Direction:     modules.DiffApply,
			SiacoinOutput: txn.SiacoinOutputs[i],
			ID:            txn.SiacoinOutputID(uint64(i)),
		}
	}
	cc := modules.ConsensusChange{
		AppliedBlocks: []types.Block{{
			Transactions: []types.Transaction{txn},
		}},
		SiacoinOutputDiffs: outputs,
	}
	fastrand.Read(cc.ID[:])
	m.subscriber.ProcessConsensusChange(cc)
}

// sendSiacoins creates an unsigned transaction that sends amount siacoins to
// dest, or false if the supplied inputs are not sufficient to fund such a
// transaction. The heuristic for selecting funding inputs is unspecified. The
// transaction returns excess siacoins to changeAddr.
func sendSiacoins(amount types.Currency, dest types.UnlockHash, feePerByte types.Currency, inputs []wallet.ValuedInput, changeAddr types.UnlockHash) (types.Transaction, bool) {
	inputs, fee, change, ok := wallet.FundTransaction(amount, feePerByte, inputs)
	if !ok {
		return types.Transaction{}, false
	}

	txn := types.Transaction{
		SiacoinInputs: make([]types.SiacoinInput, len(inputs)),
		SiacoinOutputs: []types.SiacoinOutput{
			{Value: amount, UnlockHash: dest},
			{},
		}[:1], // prevent extra allocation for change output
		MinerFees:             []types.Currency{fee},
		TransactionSignatures: make([]types.TransactionSignature, 0, len(inputs)),
	}
	for i := range txn.SiacoinInputs {
		txn.SiacoinInputs[i] = inputs[i].SiacoinInput
	}
	if !change.IsZero() {
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{
			Value:      change,
			UnlockHash: changeAddr,
		})
	}
	return txn, true
}

func runSeedServer(h http.Handler) (*SeedClient, func() error) {
	l, err := net.Listen("tcp", "localhost:9990")
	if err != nil {
		panic(err)
	}
	srv := http.Server{Handler: h}
	go srv.Serve(l)
	return NewSeedClient(l.Addr().String()), srv.Close
}

func TestSeedServer(t *testing.T) {
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

	sm := wallet.NewSeedManager(wallet.Seed{}, store.SeedIndex())
	w := wallet.NewSeedWallet(sm, store)
	cs := new(mockCS)
	cs.ConsensusSetSubscribe(w, store.ConsensusChangeID(), nil)
	ss := NewSeedServer(w, stubTpool{})
	client, stop := runSeedServer(ss)
	defer stop()

	// simulate genesis block
	cs.sendTxn(types.GenesisBlock.Transactions[0])

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

	// get an address
	addr, err := client.NextAddress()
	if err != nil {
		t.Fatal(err)
	}

	// seed index should be incremented to 1
	if seedIndex, err := client.SeedIndex(); err != nil {
		t.Fatal(err)
	} else if seedIndex != 1 {
		t.Fatal("seed index should be 1")
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

	oldConsensus, err := client.ConsensusInfo()
	if err != nil {
		t.Fatal(err)
	}

	// simulate a transaction
	cs.sendTxn(types.Transaction{
		SiacoinOutputs: []types.SiacoinOutput{
			{UnlockHash: addr, Value: types.SiacoinPrecision.Div64(2)},
			{UnlockHash: addr, Value: types.SiacoinPrecision.Div64(2)},
		},
	})

	// CCID should have changed
	if newConsensus, err := client.ConsensusInfo(); err != nil {
		t.Fatal(err)
	} else if newConsensus.CCID == oldConsensus.CCID {
		t.Fatal("ConsensusChangeID did not change")
	} else if newConsensus.Height != oldConsensus.Height+1 {
		t.Fatal("block height did not increment")
	}

	// get new balance
	if balance, err := client.Balance(); err != nil {
		t.Fatal(err)
	} else if balance.Cmp(types.SiacoinPrecision) != 0 {
		t.Fatal("balance should be 1 SC")
	}

	// transaction should appear in history
	txnHistory, err := client.Transactions(-1)
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
	if err := client.SignTransaction(&txn, nil); err != nil {
		t.Fatal(err)
	} else if err := txn.StandaloneValid(types.ASICHardforkHeight + 1); err != nil {
		t.Fatal(err)
	} else if err := client.Broadcast([]types.Transaction{txn}); err != nil {
		t.Fatal(err)
	}
	// set and retrieve a memo for the transaction
	if err := client.SetMemo(txn.ID(), []byte("test txn")); err != nil {
		t.Fatal(err)
	} else if memo, err := client.Memo(txn.ID()); err != nil {
		t.Fatal(err)
	} else if string(memo) != "test txn" {
		t.Fatal("wrong memo for transaction")
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

func TestSeedServerThreadSafety(t *testing.T) {
	store := wallet.NewEphemeralSeedStore()
	sm := wallet.NewSeedManager(wallet.Seed{}, store.SeedIndex())
	w := wallet.NewSeedWallet(sm, store)
	cs := new(mockCS)
	cs.ConsensusSetSubscribe(w, store.ConsensusChangeID(), nil)
	ss := NewSeedServer(w, stubTpool{})
	client, stop := runSeedServer(ss)
	defer stop()

	addr := sm.NextAddress()
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
		func() { client.NextAddress() },
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
