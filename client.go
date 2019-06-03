package walrus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"

	"gitlab.com/NebulousLabs/Sia/types"
	"lukechampine.com/us/wallet"
)

type genericClient struct {
	addr string
}

func (c genericClient) req(method string, route string, data, resp interface{}) error {
	var body io.Reader
	if data != nil {
		js, _ := json.Marshal(data)
		body = bytes.NewReader(js)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://%v%v", c.addr, route), body)
	if err != nil {
		panic(err)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer io.Copy(ioutil.Discard, r.Body)
	defer r.Body.Close()
	if r.StatusCode != 200 {
		err, _ := ioutil.ReadAll(r.Body)
		return errors.New(string(err))
	}
	if resp == nil {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(resp)
}

func (c genericClient) get(route string, r interface{}) error     { return c.req("GET", route, nil, r) }
func (c genericClient) post(route string, d, r interface{}) error { return c.req("POST", route, d, r) }
func (c genericClient) put(route string, d interface{}) error     { return c.req("PUT", route, d, nil) }
func (c genericClient) delete(route string) error                 { return c.req("DELETE", route, nil, nil) }

// Addresses returns all addresses known to the wallet.
func (c *genericClient) Addresses() (addrs []types.UnlockHash, err error) {
	err = c.get("/addresses", &addrs)
	return
}

// AddressInfo returns information about a specific address, including its
// unlock conditions and the index it was derived from.
func (c *genericClient) AddressInfo(addr types.UnlockHash) (info wallet.SeedAddressInfo, err error) {
	err = c.get("/addresses/"+addr.String(), &info)
	return
}

// Balance returns the current wallet balance.
func (c *genericClient) Balance() (bal types.Currency, err error) {
	err = c.get("/balance", &bal)
	return
}

// Broadcast broadcasts the supplied transaction set to all connected peers.
func (c *genericClient) Broadcast(txnSet []types.Transaction) error {
	return c.post("/broadcast", txnSet, nil)
}

// BlockRewards returns the block rewards tracked by the wallet. If max < 0, all
// rewards are returned; otherwise, at most max rewards are returned. The
// rewards are ordered newest-to-oldest.
func (c *genericClient) BlockRewards(max int) (rewards []wallet.BlockReward, err error) {
	err = c.get("/blockrewards?max="+strconv.Itoa(max), &rewards)
	return
}

// ConsensusInfo returns the current blockchain height and consensus change ID.
// The latter is a unique ID that changes whenever blocks are added to the
// blockchain.
func (c *genericClient) ConsensusInfo() (info ResponseConsensus, err error) {
	err = c.get("/consensus", &info)
	return
}

// RecommendedFee returns the current recommended transaction fee in hastings
// per byte of the Sia-encoded transaction.
func (c *genericClient) RecommendedFee() (fee types.Currency, err error) {
	err = c.get("/fee", &fee)
	return
}

// FileContracts returns the file contracts tracked by the wallet. If max < 0,
// all contracts are returned; otherwise, at most max contracts are returned.
// The contracts are ordered newest-to-oldest.
func (c *genericClient) FileContracts(max int) (contracts []wallet.FileContract, err error) {
	err = c.get("/filecontracts?max="+strconv.Itoa(max), &contracts)
	return
}

// FileContractHistory returns the revision history of the specified file
// contract, which must be a contract tracked by the wallet.
func (c *genericClient) FileContractHistory(id types.FileContractID) (history []wallet.FileContract, err error) {
	err = c.get("/filecontracts/"+id.String(), &history)
	return
}

// LimboOutputs returns outputs that are in Limbo.
func (c *genericClient) LimboOutputs() (outputs []wallet.LimboOutput, err error) {
	err = c.get("/limbo", &outputs)
	return
}

// MoveToLimbo places an output in Limbo. The output will no longer be returned
// by Outputs or contribute to the wallet's balance.
//
// Manually moving outputs to Limbo is typically unnecessary. Calling Broadcast
// will move the relevant outputs to Limbo automatically.
func (c *genericClient) MoveToLimbo(id types.SiacoinOutputID) (err error) {
	return c.put("/limbo/"+id.String(), nil)
}

// RemoveFromLimbo removes an output from Limbo.
//
// Manually removing outputs from Limbo is typically unnecessary. When a valid
// block spends an output, it will be removed from Limbo automatically.
func (c *genericClient) RemoveFromLimbo(id types.SiacoinOutputID) (err error) {
	return c.delete("/limbo/" + id.String())
}

// Memo retrieves the memo for a transaction.
func (c *genericClient) Memo(txid types.TransactionID) (memo []byte, err error) {
	resp, err := http.Get(fmt.Sprintf("http://%v/memos/%v", c.addr, txid.String()))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, errors.New(string(data))
	}
	return data, nil
}

// SetMemo adds a memo for a transaction, overwriting the previous memo if it
// exists.
//
// Memos are not stored on the blockchain. They exist only in the local wallet.
func (c *genericClient) SetMemo(txid types.TransactionID, memo []byte) (err error) {
	req, err := http.NewRequest("PUT", fmt.Sprintf("http://%v/memos/%v", c.addr, txid.String()), bytes.NewReader(memo))
	if err != nil {
		panic(err)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer io.Copy(ioutil.Discard, r.Body)
	defer r.Body.Close()
	if r.StatusCode != 200 {
		err, _ := ioutil.ReadAll(r.Body)
		return errors.New(string(err))
	}
	return nil
}

// Transactions lists the IDs of transactions relevant to the wallet. If max <
// 0, all such IDs are returned; otherwise, at most max IDs are returned. The
// IDs are ordered newest-to-oldest.
func (c *genericClient) Transactions(max int) (txids []types.TransactionID, err error) {
	err = c.get("/transactions?max="+strconv.Itoa(max), &txids)
	return
}

// Transactions lists the IDs of transactions relevant to the specified address,
// which must be owned by the wallet. If max < 0, all such IDs are returned;
// otherwise, at most max IDs are returned. The IDs are ordered
// newest-to-oldest.
func (c *genericClient) TransactionsByAddress(addr types.UnlockHash, max int) (txids []types.TransactionID, err error) {
	err = c.get("/transactions?max="+strconv.Itoa(max)+"&addr="+addr.String(), &txids)
	return
}

// Transaction returns the transaction with the specified ID, as well as inflow,
// outflow, and fee information. The transaction must be relevant to the wallet.
func (c *genericClient) Transaction(txid types.TransactionID) (txn ResponseTransactionsID, err error) {
	err = c.get("/transactions/"+txid.String(), &txn)
	return
}

// UnspentOutputs returns the outputs that the wallet can spend.
func (c *genericClient) UnspentOutputs() (utxos []UTXO, err error) {
	err = c.get("/utxos", &utxos)
	return
}

// A SeedClient is a client for a SeedServer.
type SeedClient struct {
	genericClient
}

// NextAddress generates a new address from the wallet's seed.
func (c *SeedClient) NextAddress() (addr types.UnlockHash, err error) {
	err = c.post("/nextaddress", nil, &addr)
	return
}

// SeedIndex returns the wallet's current seed index. This index will be used to
// derive the next address.
func (c *SeedClient) SeedIndex() (index uint64, err error) {
	err = c.get("/seedindex", &index)
	return
}

// SignTransaction signs the specified transaction using keys owned by the
// wallet. If toSign is nil, SignTransaction will automatically add
// TransactionSignatures for each input owned by the SeedManager. If toSign is
// not nil, it a list of indices of TransactionSignatures already present in
// txn; SignTransaction will fill in the Signature field of each.
func (c *SeedClient) SignTransaction(txn *types.Transaction, toSign []int) (err error) {
	return c.post("/sign", RequestSign{
		Transaction: *txn,
		ToSign:      toSign,
	}, txn)
}

// NewSeedClient returns a SeedClient communicating with a SeedServer listening
// on the specified address.
func NewSeedClient(addr string) *SeedClient {
	return &SeedClient{genericClient{addr}}
}

// WatchSeedClient is a client for a WatchSeedServer.
type WatchSeedClient struct {
	genericClient
}

// WatchAddress adds a set of address metadata to the wallet. Future
// transactions and outputs relevant to this address will be considered relevant
// to the wallet.
//
// Importing an address does NOT import transactions and outputs relevant to
// that address that are already in the blockchain.
func (c *WatchSeedClient) WatchAddress(info wallet.SeedAddressInfo) error {
	return c.post("/addresses", info, new(types.UnlockHash))
}

// UnwatchAddress removes an address from the wallet. Future transactions and
// outputs relevant to this address will not be considered relevant to the
// wallet.
//
// Removing an address does NOT remove transactions and outputs relevant to that
// address that are already recorded in the wallet.
func (c *WatchSeedClient) UnwatchAddress(addr types.UnlockHash) error {
	return c.delete("/addresses/" + addr.String())
}

// NewWatchSeedClient returns a WatchSeedClient communicating with a
// WatchSeedServer listening on the specified address.
func NewWatchSeedClient(addr string) *WatchSeedClient {
	return &WatchSeedClient{genericClient{addr}}
}
