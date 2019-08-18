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
	subscriber    modules.ConsensusSetSubscriber
	dscos         map[types.BlockHeight][]modules.DelayedSiacoinOutputDiff
	filecontracts map[types.FileContractID]types.FileContract
	height        types.BlockHeight
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
	frand.Read(cc.ID[:])
	m.subscriber.ProcessConsensusChange(cc)
	m.height++
}

func (m *mockCS) mineBlock(fees types.Currency, addr types.UnlockHash) {
	b := types.Block{
		Transactions: []types.Transaction{{
			MinerFees: []types.Currency{fees},
		}},
		MinerPayouts: []types.SiacoinOutput{
			{UnlockHash: addr},
		},
	}
	b.MinerPayouts[0].Value = b.CalculateSubsidy(0)
	cc := modules.ConsensusChange{
		AppliedBlocks: []types.Block{b},
		DelayedSiacoinOutputDiffs: []modules.DelayedSiacoinOutputDiff{{
			SiacoinOutput:  b.MinerPayouts[0],
			ID:             b.MinerPayoutID(0),
			MaturityHeight: types.MaturityDelay,
		}},
	}
	for _, dsco := range m.dscos[m.height] {
		cc.SiacoinOutputDiffs = append(cc.SiacoinOutputDiffs, modules.SiacoinOutputDiff{
			Direction:     modules.DiffApply,
			SiacoinOutput: dsco.SiacoinOutput,
			ID:            dsco.ID,
		})
	}
	frand.Read(cc.ID[:])
	m.subscriber.ProcessConsensusChange(cc)
	m.height++
	if m.dscos == nil {
		m.dscos = make(map[types.BlockHeight][]modules.DelayedSiacoinOutputDiff)
	}
	dsco := cc.DelayedSiacoinOutputDiffs[0]
	m.dscos[dsco.MaturityHeight] = append(m.dscos[dsco.MaturityHeight], dsco)
}

func (m *mockCS) formContract(payout types.Currency, addr types.UnlockHash) {
	b := types.Block{
		Transactions: []types.Transaction{{
			FileContracts: []types.FileContract{{
				Payout: payout,
				ValidProofOutputs: []types.SiacoinOutput{
					{UnlockHash: addr, Value: payout},
					{},
				},
				MissedProofOutputs: []types.SiacoinOutput{
					{UnlockHash: addr, Value: payout},
					{},
				},
			}},
		}},
	}
	cc := modules.ConsensusChange{
		AppliedBlocks: []types.Block{b},
		FileContractDiffs: []modules.FileContractDiff{{
			FileContract: b.Transactions[0].FileContracts[0],
			ID:           b.Transactions[0].FileContractID(0),
			Direction:    modules.DiffApply,
		}},
	}
	frand.Read(cc.ID[:])
	m.subscriber.ProcessConsensusChange(cc)
	m.height++
	if m.filecontracts == nil {
		m.filecontracts = make(map[types.FileContractID]types.FileContract)
	}
	m.filecontracts[b.Transactions[0].FileContractID(0)] = b.Transactions[0].FileContracts[0]
}

func (m *mockCS) reviseContract(id types.FileContractID) {
	fc := m.filecontracts[id]
	delta := fc.ValidProofOutputs[0].Value.Div64(2)
	fc.ValidProofOutputs[0].Value = fc.ValidProofOutputs[0].Value.Sub(delta)
	fc.ValidProofOutputs[1].Value = fc.ValidProofOutputs[1].Value.Add(delta)
	fc.MissedProofOutputs[0].Value = fc.MissedProofOutputs[0].Value.Sub(delta)
	fc.MissedProofOutputs[1].Value = fc.MissedProofOutputs[1].Value.Add(delta)
	fc.RevisionNumber++
	b := types.Block{
		Transactions: []types.Transaction{{
			FileContractRevisions: []types.FileContractRevision{{
				ParentID:              id,
				NewFileSize:           fc.FileSize,
				NewFileMerkleRoot:     fc.FileMerkleRoot,
				NewWindowStart:        fc.WindowStart,
				NewWindowEnd:          fc.WindowEnd,
				NewValidProofOutputs:  fc.ValidProofOutputs,
				NewMissedProofOutputs: fc.MissedProofOutputs,
				NewUnlockHash:         fc.UnlockHash,
				NewRevisionNumber:     fc.RevisionNumber,
			}},
		}},
	}
	cc := modules.ConsensusChange{
		AppliedBlocks: []types.Block{b},
		FileContractDiffs: []modules.FileContractDiff{
			{
				FileContract: m.filecontracts[id],
				ID:           id,
				Direction:    modules.DiffRevert,
			},
			{
				FileContract: fc,
				ID:           id,
				Direction:    modules.DiffApply,
			},
		},
	}
	frand.Read(cc.ID[:])
	m.subscriber.ProcessConsensusChange(cc)
	m.height++
	m.filecontracts[id] = fc
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
	return NewClient(l.Addr().String()), srv.Close
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

	w := wallet.NewWatchOnlyWallet(store)
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

	// bring the transaction back from limbo
	if err := client.RemoveFromLimbo(limbo[0].ID()); err != nil {
		t.Fatal(err)
	}
	// we should have two UTXOs again
	if limbo, err := client.LimboTransactions(); err != nil {
		t.Fatal(err)
	} else if len(limbo) != 0 {
		t.Fatal("limbo should be empty")
	} else if outputs, err := client.UnspentOutputs(true); err != nil {
		t.Fatal(err)
	} else if len(outputs) != 2 {
		t.Fatal("should have two UTXOs")
	}
}

func TestServerThreadSafety(t *testing.T) {
	store := wallet.NewEphemeralStore()
	w := wallet.NewWatchOnlyWallet(store)
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
