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

func (c *genericClient) AllAddresses() (addrs []types.UnlockHash, err error) {
	err = c.get("/addresses", &addrs)
	return
}

func (c *genericClient) AddressInfo(addr types.UnlockHash) (info wallet.SeedAddressInfo, err error) {
	err = c.get("/addresses/"+addr.String(), &info)
	return
}

func (c *genericClient) Balance() (bal types.Currency, err error) {
	err = c.get("/balance", &bal)
	return
}

func (c *genericClient) Broadcast(txnSet []types.Transaction) error {
	return c.post("/broadcast", txnSet, nil)
}

func (c *genericClient) ConsensusInfo() (info ResponseConsensus, err error) {
	err = c.get("/consensus", &info)
	return
}

func (c *genericClient) RecommendedFee() (fee types.Currency, err error) {
	err = c.get("/fee", &fee)
	return
}

func (c *genericClient) LimboOutputs() (outputs []wallet.LimboOutput, err error) {
	err = c.get("/limbo", &outputs)
	return
}

func (c *genericClient) MoveToLimbo(id types.SiacoinOutputID) (err error) {
	return c.put("/limbo/"+id.String(), nil)
}

func (c *genericClient) RemoveFromLimbo(id types.SiacoinOutputID) (err error) {
	return c.delete("/limbo/" + id.String())
}

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

func (c *genericClient) Transactions(max int) (txids []types.TransactionID, err error) {
	err = c.get("/transactions?max="+strconv.Itoa(max), &txids)
	return
}

func (c *genericClient) TransactionsByAddress(addr types.UnlockHash, max int) (txids []types.TransactionID, err error) {
	err = c.get("/transactions?max="+strconv.Itoa(max)+"&addr="+addr.String(), &txids)
	return
}

func (c *genericClient) Transaction(txid types.TransactionID) (txn ResponseTransactionsID, err error) {
	err = c.get("/transactions/"+txid.String(), &txn)
	return
}

func (c *genericClient) UnspentOutputs() (utxos []SeedUTXO, err error) {
	err = c.get("/utxos", &utxos)
	return
}

type SeedClient struct {
	genericClient
}

func (c *SeedClient) NextAddress() (addr types.UnlockHash, err error) {
	err = c.post("/nextaddress", nil, &addr)
	return
}

func (c *SeedClient) SeedIndex() (index uint64, err error) {
	err = c.get("/seedindex", &index)
	return
}

func (c *SeedClient) SignTransaction(txn *types.Transaction, toSign []int) (err error) {
	return c.post("/sign", RequestSign{
		Transaction: *txn,
		ToSign:      toSign,
	}, txn)
}

func NewSeedClient(addr string) *SeedClient {
	return &SeedClient{genericClient{addr}}
}

type WatchSeedClient struct {
	genericClient
}

func (c *WatchSeedClient) WatchAddress(info wallet.SeedAddressInfo) error {
	return c.post("/addresses", info, new(types.UnlockHash))
}

func (c *WatchSeedClient) UnwatchAddress(addr types.UnlockHash) error {
	return c.delete("/addresses/" + addr.String())
}

func NewWatchSeedClient(addr string) *WatchSeedClient {
	return &WatchSeedClient{genericClient{addr}}
}
