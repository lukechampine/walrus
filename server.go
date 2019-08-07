package walrus

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"reflect"
	"strconv"
	"unsafe"

	"github.com/julienschmidt/httprouter"
	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/types"
	"lukechampine.com/us/wallet"
)

// A TransactionPool can broadcast transactions and estimate transaction
// fees.
type TransactionPool interface {
	AcceptTransactionSet([]types.Transaction) error
	FeeEstimation() (min types.Currency, max types.Currency)
}

func writeJSON(w io.Writer, v interface{}) {
	// encode nil slices as [] instead of null
	if val := reflect.ValueOf(v); val.Kind() == reflect.Slice && val.Len() == 0 {
		w.Write([]byte("[]\n"))
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "\t")
	enc.Encode(v)
}

type genericWallet interface {
	Addresses() []types.UnlockHash
	AddressInfo(addr types.UnlockHash) (wallet.SeedAddressInfo, bool)
	Balance(limbo bool) types.Currency
	BlockRewards(n int) []wallet.BlockReward
	ChainHeight() types.BlockHeight
	ConsensusChangeID() modules.ConsensusChangeID
	FileContracts(n int) []wallet.FileContract
	FileContractHistory(id types.FileContractID) []wallet.FileContract
	LimboTransactions() []wallet.LimboTransaction
	AddToLimbo(txn types.Transaction)
	RemoveFromLimbo(txid types.TransactionID)
	Memo(txid types.TransactionID) []byte
	OwnsAddress(addr types.UnlockHash) bool
	SetMemo(txid types.TransactionID, memo []byte)
	Transaction(id types.TransactionID) (types.Transaction, bool)
	Transactions(n int) []types.TransactionID
	TransactionsByAddress(addr types.UnlockHash, n int) []types.TransactionID
	UnspentOutputs(limbo bool) []wallet.UnspentOutput
}

type genericServer struct {
	w  genericWallet
	tp TransactionPool
}

func (s *genericServer) addressesHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	writeJSON(w, s.w.Addresses())
}

func (s *genericServer) addressesaddrHandlerGET(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var addr types.UnlockHash
	if err := addr.LoadString(ps.ByName("addr")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, ok := s.w.AddressInfo(addr)
	if !ok {
		http.Error(w, "No such entry", http.StatusNotFound)
		return
	}
	writeJSON(w, responseAddressesAddr(info))
}

func (s *genericServer) balanceHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	limbo := req.FormValue("limbo") == "true"
	writeJSON(w, s.w.Balance(limbo))
}

func (s *genericServer) blockrewardsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	max := -1
	if req.FormValue("max") != "" {
		var err error
		max, err = strconv.Atoi(req.FormValue("max"))
		if err != nil {
			http.Error(w, "Invalid 'max' value: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	writeJSON(w, responseBlockRewards(s.w.BlockRewards(max)))
}

func (s *genericServer) broadcastHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var txnSet []types.Transaction
	if err := json.NewDecoder(req.Body).Decode(&txnSet); err != nil {
		http.Error(w, "Could not parse transaction: "+err.Error(), http.StatusBadRequest)
		return
	} else if len(txnSet) == 0 {
		http.Error(w, "Transaction set is empty", http.StatusBadRequest)
		return
	}
	// check for duplicate transactions
	for _, txn := range txnSet {
		if _, ok := s.w.Transaction(txn.ID()); ok {
			http.Error(w, "Transaction "+txn.ID().String()+" is already in the blockchain", http.StatusBadRequest)
			return
		}
	}

	// submit the transaction set (ignoring duplicate error -- if the set is
	// already in the tpool, great)
	err := s.tp.AcceptTransactionSet(txnSet)
	if err != nil && err != modules.ErrDuplicateTransactionSet {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// add the transactions to Limbo
	for _, txn := range txnSet {
		s.w.AddToLimbo(txn)
	}
}

func (s *genericServer) consensusHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	writeJSON(w, ResponseConsensus{
		Height: s.w.ChainHeight(),
		CCID:   crypto.Hash(s.w.ConsensusChangeID()),
	})
}

func (s *genericServer) feeHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	median, _ := s.tp.FeeEstimation()
	writeJSON(w, median)
}

func (s *genericServer) filecontractsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	max := -1
	if req.FormValue("max") != "" {
		var err error
		max, err = strconv.Atoi(req.FormValue("max"))
		if err != nil {
			http.Error(w, "Invalid 'max' value: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	writeJSON(w, responseFileContracts(s.w.FileContracts(max)))
}

func (s *genericServer) filecontractsidHandler(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var id types.FileContractID
	if err := id.LoadString(ps.ByName("id")); err != nil {
		http.Error(w, "Invalid ID: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, responseFileContracts(s.w.FileContractHistory(id)))
}

func (s *genericServer) limboHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	writeJSON(w, responseLimbo(s.w.LimboTransactions()))
}

func (s *genericServer) limboHandlerPUT(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var txn types.Transaction
	if err := json.NewDecoder(req.Body).Decode(&txn); err != nil {
		http.Error(w, "Could not parse transaction: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.w.AddToLimbo(txn)
}

func (s *genericServer) limboHandlerDELETE(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var txid types.TransactionID
	if err := (*crypto.Hash)(&txid).LoadString(ps.ByName("id")); err != nil {
		http.Error(w, "Invalid ID: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.w.RemoveFromLimbo(txid)
}

func (s *genericServer) memosHandlerPUT(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var txid types.TransactionID
	if err := (*crypto.Hash)(&txid).LoadString(ps.ByName("txid")); err != nil {
		http.Error(w, "Invalid transaction ID: "+err.Error(), http.StatusBadRequest)
		return
	}
	body, err := ioutil.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "Couldn't read memo: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.w.SetMemo(txid, body)
}

func (s *genericServer) memosHandlerGET(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var txid types.TransactionID
	if err := (*crypto.Hash)(&txid).LoadString(ps.ByName("txid")); err != nil {
		http.Error(w, "Invalid transaction ID: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Write(s.w.Memo(txid))
}

func (s *genericServer) transactionsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	max := -1 // all txns
	if req.FormValue("max") != "" {
		var err error
		max, err = strconv.Atoi(req.FormValue("max"))
		if err != nil {
			http.Error(w, "Invalid 'max' value: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	var resp []types.TransactionID
	if req.FormValue("addr") != "" {
		var addr types.UnlockHash
		if err := addr.LoadString(req.FormValue("addr")); err != nil {
			http.Error(w, "Invalid address: "+err.Error(), http.StatusBadRequest)
			return
		}
		resp = s.w.TransactionsByAddress(addr, max)
	} else {
		resp = s.w.Transactions(max)
	}
	writeJSON(w, resp)
}

func (s *genericServer) transactionsidHandler(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var txid crypto.Hash
	if err := txid.LoadString(ps.ByName("txid")); err != nil {
		http.Error(w, "Invalid transaction ID: "+err.Error(), http.StatusBadRequest)
		return
	}
	txn, ok := s.w.Transaction(types.TransactionID(txid))
	if !ok {
		http.Error(w, "Transaction not found", http.StatusNotFound)
		return
	}
	// calculate inflow/outflow/fee
	var inflow, outflow, fee types.Currency
	for _, sco := range txn.SiacoinOutputs {
		if s.w.OwnsAddress(sco.UnlockHash) {
			inflow = inflow.Add(sco.Value)
		} else {
			outflow = outflow.Add(sco.Value)
		}
	}
	for _, c := range txn.MinerFees {
		fee = fee.Add(c)
	}
	outflow = outflow.Add(fee)
	writeJSON(w, ResponseTransactionsID{
		Transaction: txn,
		Inflow:      inflow,
		Outflow:     outflow,
		FeePerByte:  fee.Div64(uint64(txn.MarshalSiaSize())),
	})
}

func (s *genericServer) unconfirmedparentsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var txn types.Transaction
	if err := json.NewDecoder(req.Body).Decode(&txn); err != nil {
		http.Error(w, "Could not parse transaction: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, wallet.UnconfirmedParents(txn, s.w.LimboTransactions()))
}

func (s *genericServer) utxosHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	limbo := req.FormValue("limbo") == "true"
	outputs := s.w.UnspentOutputs(limbo)
	utxos := make([]UTXO, len(outputs))
	for i, o := range outputs {
		info, ok := s.w.AddressInfo(o.UnlockHash)
		if !ok {
			panic("missing info for " + o.UnlockHash.String())
		}
		utxos[i] = UTXO{
			ID:               o.ID,
			Value:            o.Value,
			UnlockConditions: info.UnlockConditions,
			UnlockHash:       o.UnlockHash,
			KeyIndex:         info.KeyIndex,
		}
	}
	writeJSON(w, utxos)
}

// Generic API

func newGenericServer(w genericWallet, tp TransactionPool) *httprouter.Router {
	s := genericServer{
		w:  w,
		tp: tp,
	}
	mux := httprouter.New()
	mux.GET("/addresses", s.addressesHandler)
	mux.GET("/addresses/:addr", s.addressesaddrHandlerGET)
	mux.GET("/balance", s.balanceHandler)
	mux.GET("/blockrewards", s.blockrewardsHandler)
	mux.POST("/broadcast", s.broadcastHandler)
	mux.GET("/consensus", s.consensusHandler)
	mux.GET("/fee", s.feeHandler)
	mux.GET("/filecontracts", s.filecontractsHandler)
	mux.GET("/filecontracts/:id", s.filecontractsidHandler)
	mux.PUT("/limbo/:id", s.limboHandlerPUT)
	mux.GET("/limbo", s.limboHandler)
	mux.DELETE("/limbo/:id", s.limboHandlerDELETE)
	mux.PUT("/memos/:txid", s.memosHandlerPUT)
	mux.GET("/memos/:txid", s.memosHandlerGET)
	mux.GET("/transactions", s.transactionsHandler)
	mux.GET("/transactions/:txid", s.transactionsidHandler)
	mux.POST("/unconfirmedparents", s.unconfirmedparentsHandler)
	mux.GET("/utxos", s.utxosHandler)
	return mux
}

// Hot Wallet API

type seedServer struct {
	w *wallet.SeedWallet
}

func (s *seedServer) nextaddressHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	writeJSON(w, s.w.NextAddress())
}

func (s *seedServer) seedindexHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	writeJSON(w, s.w.SeedIndex())
}

func (s *seedServer) signHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var rs RequestSign
	if err := json.NewDecoder(req.Body).Decode(&rs); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	} else if err := s.w.SignTransaction(&rs.Transaction, rs.ToSign); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, (*encodedTransaction)(unsafe.Pointer(&rs.Transaction)))
}

// NewSeedServer returns an HTTP handler that serves the seed wallet API.
func NewSeedServer(w *wallet.SeedWallet, tp TransactionPool) http.Handler {
	s := &seedServer{w}
	mux := newGenericServer(w, tp)
	mux.POST("/nextaddress", s.nextaddressHandler)
	mux.GET("/seedindex", s.seedindexHandler)
	mux.POST("/sign", s.signHandler)
	return mux
}

// Watch-Only Wallet API

// need to override (WatchOnlyWallet).AddressInfo to satisfy genericWallet
type watchSeedInfoWallet struct {
	*wallet.WatchOnlyWallet
}

func (w watchSeedInfoWallet) AddressInfo(addr types.UnlockHash) (wallet.SeedAddressInfo, bool) {
	info := w.WatchOnlyWallet.AddressInfo(addr)
	if info == nil {
		return wallet.SeedAddressInfo{}, false
	}
	var entry wallet.SeedAddressInfo
	if err := encoding.Unmarshal(info, &entry); err != nil {
		panic(err)
	}
	return entry, true
}

type watchSeedServer struct {
	w *wallet.WatchOnlyWallet
}

func (s *watchSeedServer) addressesHandlerPOST(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var info wallet.SeedAddressInfo
	if err := json.NewDecoder(req.Body).Decode(&info); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	addr := info.UnlockConditions.UnlockHash()
	s.w.AddAddress(addr, encoding.Marshal(info))
	writeJSON(w, addr)
}

func (s *watchSeedServer) addressesaddrHandlerDELETE(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var addr types.UnlockHash
	if err := addr.LoadString(ps.ByName("addr")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.w.RemoveAddress(addr)
}

// NewWatchSeedServer returns an HTTP handler that serves the watch-only
// seed-based wallet API.
func NewWatchSeedServer(w *wallet.WatchOnlyWallet, tp TransactionPool) http.Handler {
	s := &watchSeedServer{w}
	mux := newGenericServer(watchSeedInfoWallet{w}, tp)
	mux.POST("/addresses", s.addressesHandlerPOST)
	mux.DELETE("/addresses/:addr", s.addressesaddrHandlerDELETE)
	return mux
}
