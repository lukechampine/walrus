package walrus

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"reflect"
	"strconv"

	"github.com/julienschmidt/httprouter"
	"gitlab.com/NebulousLabs/Sia/crypto"
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

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	// encode nil slices as [] and nil maps as {} (instead of null)
	if val := reflect.ValueOf(v); val.Kind() == reflect.Slice && val.Len() == 0 {
		w.Write([]byte("[]\n"))
		return
	} else if val.Kind() == reflect.Map && val.Len() == 0 {
		w.Write([]byte("{}\n"))
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "\t")
	enc.Encode(v)
}

func calculateFlows(txn wallet.Transaction, owner wallet.AddressOwner) (credit, debit types.Currency) {
	for i, sci := range txn.SiacoinInputs {
		if owner.OwnsAddress(wallet.CalculateUnlockHash(sci.UnlockConditions)) {
			debit = debit.Add(txn.InputValues[i])
		}
	}
	for _, sco := range txn.SiacoinOutputs {
		if owner.OwnsAddress(sco.UnlockHash) {
			credit = credit.Add(sco.Value)
		}
	}
	return
}

type server struct {
	w  *wallet.SeedWallet
	tp TransactionPool
}

func (s *server) addressesHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	writeJSON(w, s.w.Addresses())
}

func (s *server) addressesaddrHandlerGET(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
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

func (s *server) addressesHandlerPOST(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var info wallet.SeedAddressInfo
	if err := json.NewDecoder(req.Body).Decode(&info); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.w.AddAddress(info)
	writeJSON(w, wallet.CalculateUnlockHash(info.UnlockConditions))
}

func (s *server) addressesaddrHandlerDELETE(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var addr types.UnlockHash
	if err := addr.LoadString(ps.ByName("addr")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.w.RemoveAddress(addr)
}

func (s *server) balanceHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	limbo := req.FormValue("limbo") == "true"
	writeJSON(w, s.w.Balance(limbo))
}

func (s *server) batchqueryHandler(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	switch ps.ByName("endpoint") {
	case "addresses":
		var addrs []types.UnlockHash
		if err := json.NewDecoder(req.Body).Decode(&addrs); err != nil {
			http.Error(w, "Could not parse addrs: "+err.Error(), http.StatusBadRequest)
			return
		}
		infos := make(responseBatchqueryAddresses, len(addrs))
		for _, addr := range addrs {
			if info, ok := s.w.AddressInfo(addr); ok {
				infos[addr] = info
			}
		}
		writeJSON(w, infos)
	case "transactions":
		var ids []types.TransactionID
		if err := json.NewDecoder(req.Body).Decode(&ids); err != nil {
			http.Error(w, "Could not parse ids: "+err.Error(), http.StatusBadRequest)
			return
		}
		txns := make(responseBatchqueryTransactions, len(ids))
		for _, id := range ids {
			if txn, ok := s.w.Transaction(id); ok {
				credit, debit := calculateFlows(txn, s.w)
				txns[id] = ResponseTransactionsID{
					Transaction: txn.Transaction,
					BlockID:     txn.BlockID,
					BlockHeight: txn.BlockHeight,
					Timestamp:   txn.Timestamp,
					FeePerByte:  txn.FeePerByte,
					Credit:      credit,
					Debit:       debit,
				}
			}
		}
		writeJSON(w, txns)
	default:
		http.Error(w, "batchquery endpoint must be one of: addresses, transactions", http.StatusNotFound)
	}
}

func (s *server) blockrewardsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
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

func (s *server) broadcastHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var txnSet []types.Transaction
	if err := json.NewDecoder(req.Body).Decode(&txnSet); err != nil {
		http.Error(w, "Could not parse transaction: "+err.Error(), http.StatusBadRequest)
		return
	} else if len(txnSet) == 0 {
		http.Error(w, "Transaction set is empty", http.StatusBadRequest)
		return
	}
	// if transaction set in already on-chain, no-op
	allConfirmed := true
	for _, txn := range txnSet {
		_, ok := s.w.Transaction(txn.ID())
		allConfirmed = allConfirmed && ok
	}
	if allConfirmed {
		return
	}

	// submit the transaction set (ignoring duplicate error -- if the set is
	// already in the tpool, great)
	err := s.tp.AcceptTransactionSet(txnSet)
	if err != nil && err != modules.ErrDuplicateTransactionSet {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// add transactions to Limbo
	//
	// NOTE: we only add transactions that are relevant to the wallet.
	// Otherwise, we would not be able to automatically recognize and remove
	// these transactions from Limbo when they later appeared in a block. (It's
	// still possible to manually add such transactions to Limbo, but they'll
	// need to be manually removed as well.)
	for _, txn := range txnSet {
		if wallet.RelevantTransaction(s.w, txn) {
			s.w.AddToLimbo(txn)
		}
	}
}

func (s *server) consensusHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	writeJSON(w, ResponseConsensus{
		Height: s.w.ChainHeight(),
		CCID:   crypto.Hash(s.w.ConsensusChangeID()),
	})
}

func (s *server) feeHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	median, _ := s.tp.FeeEstimation()
	writeJSON(w, median)
}

func (s *server) filecontractsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
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

func (s *server) filecontractsidHandler(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var id types.FileContractID
	if err := id.LoadString(ps.ByName("id")); err != nil {
		http.Error(w, "Invalid ID: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, responseFileContracts(s.w.FileContractHistory(id)))
}

func (s *server) limboHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	writeJSON(w, responseLimbo(s.w.LimboTransactions()))
}

func (s *server) limboHandlerPUT(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var txn types.Transaction
	if err := json.NewDecoder(req.Body).Decode(&txn); err != nil {
		http.Error(w, "Could not parse transaction: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.w.AddToLimbo(txn)
}

func (s *server) limboHandlerDELETE(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var txid types.TransactionID
	if err := (*crypto.Hash)(&txid).LoadString(ps.ByName("id")); err != nil {
		http.Error(w, "Invalid ID: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.w.RemoveFromLimbo(txid)
}

func (s *server) memosHandlerPUT(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
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

func (s *server) memosHandlerGET(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	var txid types.TransactionID
	if err := (*crypto.Hash)(&txid).LoadString(ps.ByName("txid")); err != nil {
		http.Error(w, "Invalid transaction ID: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Write(s.w.Memo(txid))
}

func (s *server) seedindexHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	writeJSON(w, s.w.SeedIndex())
}

func (s *server) transactionsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
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

func (s *server) transactionsidHandler(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
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
	credit, debit := calculateFlows(txn, s.w)
	writeJSON(w, ResponseTransactionsID{
		Transaction: txn.Transaction,
		BlockID:     txn.BlockID,
		BlockHeight: txn.BlockHeight,
		Timestamp:   txn.Timestamp,
		FeePerByte:  txn.FeePerByte,
		Credit:      credit,
		Debit:       debit,
	})
}

func (s *server) unconfirmedparentsHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	var txn types.Transaction
	if err := json.NewDecoder(req.Body).Decode(&txn); err != nil {
		http.Error(w, "Could not parse transaction: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, wallet.UnconfirmedParents(txn, s.w.LimboTransactions()))
}

func (s *server) utxosHandler(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	writeJSON(w, s.w.UnspentOutputs(req.FormValue("limbo") == "true"))
}

// NewServer returns an HTTP handler that serves the walrus API.
func NewServer(w *wallet.SeedWallet, tp TransactionPool) http.Handler {
	s := server{
		w:  w,
		tp: tp,
	}
	mux := httprouter.New()
	mux.GET("/addresses", s.addressesHandler)
	mux.POST("/addresses", s.addressesHandlerPOST)
	mux.GET("/addresses/:addr", s.addressesaddrHandlerGET)
	mux.DELETE("/addresses/:addr", s.addressesaddrHandlerDELETE)
	mux.GET("/balance", s.balanceHandler)
	mux.POST("/batchquery/:endpoint", s.batchqueryHandler)
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
	mux.GET("/seedindex", s.seedindexHandler)
	mux.GET("/transactions", s.transactionsHandler)
	mux.GET("/transactions/:txid", s.transactionsidHandler)
	mux.POST("/unconfirmedparents", s.unconfirmedparentsHandler)
	mux.GET("/utxos", s.utxosHandler)
	return mux
}
