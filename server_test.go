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
	"lukechampine.com/frand"
	"lukechampine.com/us/wallet"
)

type stubTpool struct{}

func (stubTpool) AcceptTransactionSet([]types.Transaction) (err error)   { return }
func (stubTpool) FeeEstimation() (min, max types.Currency)               { return }
func (stubTpool) TransactionSet(id crypto.Hash) (ts []types.Transaction) { return }

type mockCS struct {
	subscriber modules.ConsensusSetSubscriber
	utxos      map[types.SiacoinOutputID]types.SiacoinOutput
}

func (m *mockCS) ConsensusSetSubscribe(s modules.ConsensusSetSubscriber, ccid modules.ConsensusChangeID, cancel <-chan struct{}) error {
	m.subscriber = s
	m.utxos = make(map[types.SiacoinOutputID]types.SiacoinOutput)
	return nil
}

func (m *mockCS) sendTxn(txn types.Transaction) {
	inputs := make([]modules.SiacoinOutputDiff, len(txn.SiacoinInputs))
	for i := range inputs {
		inputs[i] = modules.SiacoinOutputDiff{
			Direction:     modules.DiffRevert,
			SiacoinOutput: m.utxos[txn.SiacoinInputs[i].ParentID],
			ID:            txn.SiacoinInputs[i].ParentID,
		}
	}
	outputs := make([]modules.SiacoinOutputDiff, len(txn.SiacoinOutputs))
	for i := range outputs {
		outputs[i] = modules.SiacoinOutputDiff{
			Direction:     modules.DiffApply,
			SiacoinOutput: txn.SiacoinOutputs[i],
			ID:            txn.SiacoinOutputID(uint64(i)),
		}
		m.utxos[outputs[i].ID] = txn.SiacoinOutputs[i]
	}
	fcs := make([]modules.FileContractDiff, len(txn.FileContracts))
	for i := range fcs {
		fcs[i] = modules.FileContractDiff{
			Direction:    modules.DiffApply,
			FileContract: txn.FileContracts[i],
			ID:           txn.FileContractID(uint64(i)),
		}
	}
	cc := modules.ConsensusChange{
		AppliedBlocks: []types.Block{{
			Transactions: []types.Transaction{txn},
		}},
		ConsensusChangeDiffs: modules.ConsensusChangeDiffs{
			SiacoinOutputDiffs: append(inputs, outputs...),
		},
	}
	frand.Read(cc.ID[:])
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

func runServer(h http.Handler) (*Client, func() error) {
	l, err := net.Listen("tcp", "localhost:9990")
	if err != nil {
		panic(err)
	}
	srv := http.Server{Handler: h}
	go srv.Serve(l)
	return NewClient("http://" + l.Addr().String()), srv.Close
}

func TestServer(t *testing.T) {
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

	w := wallet.New(store)
	cs := new(mockCS)
	cs.ConsensusSetSubscribe(w.ConsensusSetSubscriber(store), store.ConsensusChangeID(), nil)
	client, stop := runServer(NewServer(w, stubTpool{}))
	defer stop()

	// initial balance should be zero
	if balance, err := client.Balance(false); err != nil {
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
	if err := client.AddAddress(addrInfo); err != nil {
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
	if balance, err := client.Balance(false); err != nil {
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
	} else if htx.BlockHeight != 1 {
		t.Fatal("transaction height should be 1")
	}

	// create an unsigned transaction using available outputs
	outputs, err := client.UnspentOutputs(false)
	if err != nil {
		t.Fatal(err)
	} else if len(outputs) != 2 {
		t.Fatal("should have two UTXOs")
	}

	inputs := make([]wallet.ValuedInput, len(outputs))
	for i, o := range outputs {
		info, err := client.AddressInfo(o.UnlockHash)
		if err != nil {
			t.Fatal(err)
		}
		inputs[i] = wallet.ValuedInput{
			SiacoinInput: types.SiacoinInput{
				ParentID:         o.ID,
				UnlockConditions: info.UnlockConditions,
			},
			Value: o.Value,
		}
	}
	amount := types.SiacoinPrecision.Div64(2)
	dest := types.UnlockHash{}
	feePerByte := types.NewCurrency64(10)
	txn, ok := sendSiacoins(amount, dest, feePerByte, inputs, addr)
	if !ok {
		t.Fatal("insufficient funds")
	}

	// sign and broadcast the transaction, but do not call cs.sendTxn; we want
	// the transaction to be in limbo
	for _, sci := range txn.SiacoinInputs {
		txnSig := wallet.StandardTransactionSignature(crypto.Hash(sci.ParentID))
		wallet.AppendTransactionSignature(&txn, txnSig, seed.SecretKey(0))
	}
	if err := txn.StandaloneValid(types.FoundationHardforkHeight + 1); err != nil {
		t.Fatal(err)
	} else if err := client.Broadcast([]types.Transaction{txn}); err != nil {
		t.Fatal(err)
	}

	// with limbo transactions applied, we should only have one UTXO (the change
	// output created by the transaction)
	if outputs, err := client.UnspentOutputs(true); err != nil {
		t.Fatal(err)
	} else if len(outputs) != 1 {
		t.Fatal("should have one UTXO")
	}

	// the spent outputs should appear in the limbo transaction
	limbo, err := client.LimboTransactions()
	if err != nil {
		t.Fatal(err)
	} else if len(limbo) != 1 {
		t.Fatal("should have one transaction in limbo")
	} else if len(limbo[0].SiacoinInputs) != 2 {
		t.Fatal("limbo transaction should have two inputs", len(limbo[0].SiacoinOutputs))
	}

	// send the transaction, bringing it out of limbo
	cs.sendTxn(txn)
	// we should have 1 UTXO now (the change output)
	if limbo, err := client.LimboTransactions(); err != nil {
		t.Fatal(err)
	} else if len(limbo) != 0 {
		t.Fatal("limbo should be empty")
	} else if outputs, err := client.UnspentOutputs(true); err != nil {
		t.Fatal(err)
	} else if len(outputs) != 1 {
		t.Fatal("should have one UTXO", outputs)
	}

	// send a file contract
	cs.sendTxn(types.Transaction{
		FileContracts: []types.FileContract{{
			FileMerkleRoot:    crypto.Hash{1, 2, 3},
			ValidProofOutputs: []types.SiacoinOutput{{UnlockHash: addr}},
		}},
	})

	// query for the contract
	fcs, err := client.FileContracts(-1)
	if err != nil {
		t.Fatal(err)
	} else if len(fcs) != 1 {
		t.Fatal("expected 1 id")
	} else if fcs[0].FileMerkleRoot != (crypto.Hash{1, 2, 3}) {
		t.Fatal("contract has wrong Merkle root")
	} else if fch, err := client.FileContractHistory(fcs[0].ID); err != nil {
		t.Fatal(err)
	} else if len(fch) != 1 {
		t.Fatal("expected 1 contract in history")
	}

	// batch query addrs
	addrs, err := client.Addresses()
	if err != nil {
		t.Fatal(err)
	}
	infos, err := client.BatchAddresses(addrs)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Error("expected 1 address info")
	}
	if addrInfo := infos[addr]; addrInfo.KeyIndex != 0 || addrInfo.UnlockConditions.UnlockHash() != addr {
		t.Error("address info is inaccurate")
	}

	// batch query txns
	txnHistory, err = client.Transactions(-1)
	if err != nil {
		t.Fatal(err)
	} else if len(txnHistory) != 3 {
		t.Fatal("expected 3 transactions")
	}
	txns, err := client.BatchTransactions(txnHistory)
	if err != nil {
		t.Fatal(err)
	}

	if txn1 := txns[txnHistory[0]]; len(txn1.Transaction.FileContracts) != 1 {
		t.Error("transaction should have a file contract")
	} else if txn1.BlockHeight != 2 {
		t.Error("transaction height should be 2")
	} else if !txn1.FeePerByte.IsZero() {
		t.Error("transaction fee should be zero")
	}
	if txn2 := txns[txnHistory[1]]; len(txn2.Transaction.SiacoinOutputs) != 2 {
		t.Error("transaction should have two outputs")
	} else if txn2.BlockHeight != 1 {
		t.Error("transaction height should be 1")
	} else if txn2.FeePerByte.IsZero() {
		t.Error("transaction fee should be non-zero")
	}
	if txn3 := txns[txnHistory[2]]; len(txn3.Transaction.SiacoinOutputs) != 2 {
		t.Error("transaction should have two outputs")
	} else if txn3.BlockHeight != 1 {
		t.Error("transaction height should be 1")
	} else if !txn3.FeePerByte.IsZero() {
		t.Error("transaction fee should be zero")
	}

	// test the interface adaptor
	pclient := client.ProtoWallet(seed)
	txn = types.Transaction{
		SiacoinOutputs: []types.SiacoinOutput{
			{UnlockHash: types.UnlockHash{}, Value: types.SiacoinPrecision.Div64(10)},
		},
	}
	toSign, discard, err := pclient.FundTransaction(&txn, types.SiacoinPrecision.Div64(10))
	if err != nil {
		t.Fatal(err)
	}
	if err := pclient.SignTransaction(&txn, toSign); err != nil {
		t.Fatal(err)
	}
	if err := client.Broadcast([]types.Transaction{txn}); err != nil {
		t.Fatal(err)
	}
	discard()
}

func TestServerThreadSafety(t *testing.T) {
	store := wallet.NewEphemeralStore()
	w := wallet.New(store)
	cs := new(mockCS)
	cs.ConsensusSetSubscribe(w.ConsensusSetSubscriber(store), store.ConsensusChangeID(), nil)
	client, stop := runServer(NewServer(w, stubTpool{}))
	defer stop()

	randomAddr := func() (info wallet.SeedAddressInfo) {
		info.UnlockConditions = wallet.StandardUnlockConditions(wallet.NewSeed().PublicKey(0))
		return
	}
	info := randomAddr()
	addr := wallet.CalculateUnlockHash(info.UnlockConditions)
	w.AddAddress(info)

	txn := types.Transaction{
		SiacoinOutputs: []types.SiacoinOutput{
			{UnlockHash: addr, Value: types.SiacoinPrecision.Div64(2)},
		},
	}

	// create a bunch of goroutines that call routes and add transactions
	// concurrently
	funcs := []func(){
		func() { cs.sendTxn(txn) },
		func() { client.Balance(true) },
		func() { client.AddAddress(randomAddr()) },
		func() { client.RemoveAddress(randomAddr().UnlockConditions.UnlockHash()) },
		func() { client.Addresses() },
		func() { client.TransactionsByAddress(addr, 2) },
	}
	var wg sync.WaitGroup
	wg.Add(len(funcs))
	for _, fn := range funcs {
		go func(fn func()) {
			for i := 0; i < 10; i++ {
				time.Sleep(time.Duration(frand.Intn(10)) * time.Millisecond)
				fn()
			}
			wg.Done()
		}(fn)
	}
	wg.Wait()
}
