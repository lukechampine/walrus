package walrus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
	"lukechampine.com/frand"
	"lukechampine.com/us/ed25519hash"
	"lukechampine.com/us/renter/proto"
	"lukechampine.com/us/wallet"
)

// A Client communicates with a walrus server.
type Client struct {
	addr string
}

func (c *Client) req(method string, route string, data, resp interface{}) error {
	var body io.Reader
	if data != nil {
		js, _ := json.Marshal(data)
		body = bytes.NewReader(js)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("%v%v", c.addr, route), body)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
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

func (c *Client) get(route string, r interface{}) error     { return c.req("GET", route, nil, r) }
func (c *Client) post(route string, d, r interface{}) error { return c.req("POST", route, d, r) }
func (c *Client) put(route string, d interface{}) error     { return c.req("PUT", route, d, nil) }
func (c *Client) delete(route string) error                 { return c.req("DELETE", route, nil, nil) }

// Addresses returns all addresses known to the wallet.
func (c *Client) Addresses() (addrs []types.UnlockHash, err error) {
	err = c.get("/addresses", &addrs)
	return
}

// AddressInfo returns information about a specific address, including its
// unlock conditions and the index it was derived from.
func (c *Client) AddressInfo(addr types.UnlockHash) (info wallet.SeedAddressInfo, err error) {
	err = c.get("/addresses/"+addr.String(), &info)
	return
}

// Balance returns the current wallet balance. If the limbo flag is true, the
// balance will reflect any transactions currently in Limbo.
func (c *Client) Balance(limbo bool) (bal types.Currency, err error) {
	err = c.get("/balance?limbo="+strconv.FormatBool(limbo), &bal)
	return
}

// BatchAddresses returns information about a set of addresses, including their
// unlock conditions and the index they were derived from. If an address is not
// found, no error is returned; the address is simply omitted from the response.
func (c *Client) BatchAddresses(addrs []types.UnlockHash) (infos map[types.UnlockHash]wallet.SeedAddressInfo, err error) {
	var m responseBatchqueryAddresses
	err = c.post("/batchquery/addresses", addrs, &m)
	return m, err
}

// BatchTransactions returns information about a set of transactions. If a
// transaction is not found, no error is returned; the transaction is simply
// omitted from the response.
func (c *Client) BatchTransactions(ids []types.TransactionID) (txns map[types.TransactionID]ResponseTransactionsID, err error) {
	var m responseBatchqueryTransactions
	err = c.post("/batchquery/transactions", ids, &m)
	return m, err
}

// Broadcast broadcasts the supplied transaction set to all connected peers.
func (c *Client) Broadcast(txnSet []types.Transaction) error {
	return c.post("/broadcast", txnSet, nil)
}

// BlockRewards returns the block rewards tracked by the wallet. If max < 0, all
// rewards are returned; otherwise, at most max rewards are returned. The
// rewards are ordered newest-to-oldest.
func (c *Client) BlockRewards(max int) (rewards []wallet.BlockReward, err error) {
	err = c.get("/blockrewards?max="+strconv.Itoa(max), &rewards)
	return
}

// ConsensusInfo returns the current blockchain height and consensus change ID.
// The latter is a unique ID that changes whenever blocks are added to the
// blockchain.
func (c *Client) ConsensusInfo() (info ResponseConsensus, err error) {
	err = c.get("/consensus", &info)
	return
}

// RecommendedFee returns the current recommended transaction fee in hastings
// per byte of the Sia-encoded transaction.
func (c *Client) RecommendedFee() (fee types.Currency, err error) {
	err = c.get("/fee", &fee)
	return
}

// FileContracts returns the file contracts tracked by the wallet. If max < 0,
// all contracts are returned; otherwise, at most max contracts are returned.
// The contracts are ordered newest-to-oldest.
func (c *Client) FileContracts(max int) (contracts []wallet.FileContract, err error) {
	err = c.get("/filecontracts?max="+strconv.Itoa(max), &contracts)
	return
}

// FileContractHistory returns the revision history of the specified file
// contract, which must be a contract tracked by the wallet.
func (c *Client) FileContractHistory(id types.FileContractID) (history []wallet.FileContract, err error) {
	err = c.get("/filecontracts/"+id.String(), &history)
	return
}

// LimboTransactions returns transactions that are in Limbo.
func (c *Client) LimboTransactions() (txns []wallet.LimboTransaction, err error) {
	err = c.get("/limbo", &txns)
	return
}

// AddToLimbo places a transaction in Limbo. The output will no longer be returned
// by Outputs or contribute to the wallet's balance.
//
// Manually adding transactions to Limbo is typically unnecessary. Calling Broadcast
// will move all transactions in the set to Limbo automatically.
func (c *Client) AddToLimbo(txn types.Transaction) (err error) {
	return c.put("/limbo/"+txn.ID().String(), txn)
}

// RemoveFromLimbo removes a transaction from Limbo.
//
// Manually removing transactions from Limbo is typically unnecessary. When a
// transaction appears in a valid block, it will be removed from Limbo
// automatically.
func (c *Client) RemoveFromLimbo(txid types.TransactionID) (err error) {
	return c.delete("/limbo/" + txid.String())
}

// Memo retrieves the memo for a transaction.
func (c *Client) Memo(txid types.TransactionID) (memo []byte, err error) {
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
func (c *Client) SetMemo(txid types.TransactionID, memo []byte) (err error) {
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

// SeedIndex returns the index that should be used to derive the next address.
func (c *Client) SeedIndex() (index uint64, err error) {
	err = c.get("/seedindex", &index)
	return
}

// Transactions lists the IDs of transactions relevant to the wallet. If max <
// 0, all such IDs are returned; otherwise, at most max IDs are returned. The
// IDs are ordered newest-to-oldest.
func (c *Client) Transactions(max int) (txids []types.TransactionID, err error) {
	err = c.get("/transactions?max="+strconv.Itoa(max), &txids)
	return
}

// TransactionsByAddress lists the IDs of transactions relevant to the specified
// address, which must be owned by the wallet. If max < 0, all such IDs are
// returned; otherwise, at most max IDs are returned. The IDs are ordered
// newest-to-oldest.
func (c *Client) TransactionsByAddress(addr types.UnlockHash, max int) (txids []types.TransactionID, err error) {
	err = c.get("/transactions?max="+strconv.Itoa(max)+"&addr="+addr.String(), &txids)
	return
}

// Transaction returns the transaction with the specified ID, as well as credit,
// debit, and fee information. The transaction must be relevant to the wallet.
func (c *Client) Transaction(txid types.TransactionID) (txn ResponseTransactionsID, err error) {
	err = c.get("/transactions/"+txid.String(), &txn)
	return
}

// UnconfirmedParents returns any parents of txn that are in Limbo. These
// transactions will need to be included in the transaction set passed to
// Broadcast.
func (c *Client) UnconfirmedParents(txn types.Transaction) (parents []wallet.LimboTransaction, err error) {
	err = c.post("/unconfirmedparents", txn, &parents)
	return
}

// UnspentOutputs returns the outputs that the wallet can spend. If the limbo
// flag is true, the outputs will reflect any transactions currently in Limbo.
func (c *Client) UnspentOutputs(limbo bool) (utxos []wallet.UnspentOutput, err error) {
	err = c.get("/utxos?limbo="+strconv.FormatBool(limbo), &utxos)
	return
}

// AddAddress adds a set of address metadata to the wallet. Future
// transactions and outputs relevant to this address will be considered relevant
// to the wallet.
//
// Importing an address does NOT import transactions and outputs relevant to
// that address that are already in the blockchain.
func (c *Client) AddAddress(info wallet.SeedAddressInfo) error {
	return c.post("/addresses", info, new(types.UnlockHash))
}

// RemoveAddress removes an address from the wallet. Future transactions and
// outputs relevant to this address will not be considered relevant to the
// wallet.
//
// Removing an address does NOT remove transactions and outputs relevant to that
// address that are already recorded in the wallet.
func (c *Client) RemoveAddress(addr types.UnlockHash) error {
	return c.delete("/addresses/" + addr.String())
}

// ProtoWallet returns a wrapped Client that implements the proto.Wallet
// interface using an in-memory seed.
func (c *Client) ProtoWallet(seed wallet.Seed) proto.Wallet {
	return &protoBridge{Client: c, seed: seed}
}

// ProtoTransactionPool returns a wrapped Client that implements the
// proto.TransactionPool interface.
func (c *Client) ProtoTransactionPool() proto.TransactionPool {
	return &protoBridge{Client: c}
}

// NewClient returns a client that communicates with a walrus server listening
// on the specified address.
func NewClient(addr string) *Client {
	// use https by default
	if !strings.HasPrefix(addr, "https://") && !strings.HasPrefix(addr, "http://") {
		addr = "https://" + addr
	}
	return &Client{addr}
}

type protoBridge struct {
	*Client
	seed wallet.Seed
}

// proto.Wallet methods

func (c *protoBridge) Address() (types.UnlockHash, error) {
	index, err := c.Client.SeedIndex()
	if err != nil {
		return types.UnlockHash{}, err
	}
	info := wallet.SeedAddressInfo{
		UnlockConditions: wallet.StandardUnlockConditions(c.seed.PublicKey(index)),
		KeyIndex:         index,
	}
	if err := c.Client.AddAddress(info); err != nil {
		return types.UnlockHash{}, err
	}
	return info.UnlockHash(), nil
}

func (c *protoBridge) FundTransaction(txn *types.Transaction, amount types.Currency) ([]crypto.Hash, error) {
	if amount.IsZero() {
		return nil, nil
	}
	// UnspentOutputs(true) returns the outputs that exist after Limbo
	// transactions are applied. This is not ideal, because the host is more
	// likely to reject transactions that have unconfirmed parents. On the other
	// hand, UnspentOutputs(false) won't return any outputs that were created
	// in Limbo transactions, but it *will* return outputs that have been
	// *spent* in Limbo transactions. So what we really want is the intersection
	// of these sets, keeping only the confirmed outputs that were not spent in
	// Limbo transactions.
	limboOutputs, err := c.Client.UnspentOutputs(true)
	if err != nil {
		return nil, err
	}
	confirmedOutputs, err := c.Client.UnspentOutputs(false)
	if err != nil {
		return nil, err
	}
	var outputs []wallet.UnspentOutput
	for _, lo := range limboOutputs {
		for _, co := range confirmedOutputs {
			if co.ID == lo.ID {
				outputs = append(outputs, lo)
				break
			}
		}
	}
	var balance types.Currency
	for _, o := range outputs {
		balance = balance.Add(o.Value)
	}
	var limboBalance types.Currency
	for _, o := range limboOutputs {
		limboBalance = limboBalance.Add(o.Value)
	}

	if balance.Cmp(amount) < 0 {
		if limboBalance.Cmp(amount) < 0 {
			return nil, wallet.ErrInsufficientFunds
		}
		// confirmed outputs are not sufficient, but limbo outputs are
		outputs = limboOutputs
	}
	// choose outputs randomly
	frand.Shuffle(len(outputs), reflect.Swapper(outputs))

	// keep adding outputs until we have enough
	var fundingOutputs []wallet.UnspentOutput
	var outputSum types.Currency
	for i, o := range outputs {
		if outputSum = outputSum.Add(o.Value); outputSum.Cmp(amount) >= 0 {
			fundingOutputs = outputs[:i+1]
			break
		}
	}
	// due to the random selection, we may have more outputs than we need; sort
	// by value and discard as many as possible
	sort.Slice(fundingOutputs, func(i, j int) bool {
		return fundingOutputs[i].Value.Cmp(fundingOutputs[j].Value) < 0
	})
	for outputSum.Sub(fundingOutputs[0].Value).Cmp(amount) >= 0 {
		outputSum = outputSum.Sub(fundingOutputs[0].Value)
		fundingOutputs = fundingOutputs[1:]
	}

	var toSign []crypto.Hash
	for _, o := range fundingOutputs {
		info, err := c.Client.AddressInfo(o.UnlockHash)
		if err != nil {
			return nil, err
		}
		txn.SiacoinInputs = append(txn.SiacoinInputs, types.SiacoinInput{
			ParentID:         o.ID,
			UnlockConditions: info.UnlockConditions,
		})
		txn.TransactionSignatures = append(txn.TransactionSignatures, wallet.StandardTransactionSignature(crypto.Hash(o.ID)))
		toSign = append(toSign, crypto.Hash(o.ID))
	}
	// add change output if needed
	if change := outputSum.Sub(amount); !change.IsZero() {
		changeAddr, err := c.Address()
		if err != nil {
			return nil, err
		}
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{
			UnlockHash: changeAddr,
			Value:      change,
		})
	}
	return toSign, nil
}

func (c *protoBridge) SignTransaction(txn *types.Transaction, toSign []crypto.Hash) error {
	if len(toSign) == 0 {
		// lazy mode: add standard sigs for every input we own
		for _, input := range txn.SiacoinInputs {
			info, err := c.Client.AddressInfo(input.UnlockConditions.UnlockHash())
			if err != nil {
				// TODO: catch errors other than "address not found"
				continue
			}
			sk := c.seed.SecretKey(info.KeyIndex)
			txnSig := wallet.StandardTransactionSignature(crypto.Hash(input.ParentID))
			wallet.AppendTransactionSignature(txn, txnSig, sk)
		}
		return nil
	}

	sigAddr := func(id crypto.Hash) (types.UnlockHash, bool) {
		for _, sci := range txn.SiacoinInputs {
			if crypto.Hash(sci.ParentID) == id {
				return sci.UnlockConditions.UnlockHash(), true
			}
		}
		for _, sfi := range txn.SiafundInputs {
			if crypto.Hash(sfi.ParentID) == id {
				return sfi.UnlockConditions.UnlockHash(), true
			}
		}
		for _, fcr := range txn.FileContractRevisions {
			if crypto.Hash(fcr.ParentID) == id {
				return fcr.UnlockConditions.UnlockHash(), true
			}
		}
		return types.UnlockHash{}, false
	}
	sign := func(i int) error {
		addr, ok := sigAddr(txn.TransactionSignatures[i].ParentID)
		if !ok {
			return errors.New("invalid id")
		}
		info, err := c.Client.AddressInfo(addr)
		if err != nil {
			return err
		}
		sk := c.seed.SecretKey(info.KeyIndex)
		txn.TransactionSignatures[i].Signature = ed25519hash.Sign(sk, txn.SigHash(i, types.ASICHardforkHeight+1))
		return nil
	}

outer:
	for _, parent := range toSign {
		for sigIndex, sig := range txn.TransactionSignatures {
			if sig.ParentID == parent {
				if err := sign(sigIndex); err != nil {
					return err
				}
				continue outer
			}
		}
		return errors.New("sighash not found in transaction")
	}

	return nil
}

// proto.TransactionPool methods

func (c *protoBridge) AcceptTransactionSet(txnSet []types.Transaction) error {
	return c.Client.Broadcast(txnSet)
}

func (c *protoBridge) UnconfirmedParents(txn types.Transaction) ([]types.Transaction, error) {
	limboParents, err := c.Client.UnconfirmedParents(txn)
	parents := make([]types.Transaction, len(limboParents))
	for i := range parents {
		parents[i] = limboParents[i].Transaction
	}
	return parents, err
}

func (c *protoBridge) FeeEstimate() (minFee, maxFee types.Currency, err error) {
	fee, err := c.Client.RecommendedFee()
	return fee, fee.Mul64(3), err
}
