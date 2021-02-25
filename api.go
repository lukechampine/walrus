// Package walrus defines a walrus server and client.
package walrus // import "lukechampine.com/walrus"

import (
	"encoding/hex"
	"encoding/json"
	"time"
	"unsafe"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/types"
	"lukechampine.com/us/wallet"
)

// JSONTransaction overrides the default JSON encoding of types.Transaction to
// use camelCase and stringified pubkeys and omit empty fields.
type JSONTransaction types.Transaction

// MarshalJSON implements json.Marshaler.
func (txn JSONTransaction) MarshalJSON() ([]byte, error) {
	type encodedTransaction struct {
		SiacoinInputs []struct {
			ParentID         types.SiacoinOutputID   `json:"parentID"`
			UnlockConditions encodedUnlockConditions `json:"unlockConditions"`
		} `json:"siacoinInputs,omitempty"`
		SiacoinOutputs []encodedSiacoinOutput `json:"siacoinOutputs,omitempty"`
		FileContracts  []struct {
			FileSize           uint64                 `json:"fileSize"`
			FileMerkleRoot     crypto.Hash            `json:"fileMerkleRoot"`
			WindowStart        types.BlockHeight      `json:"windowStart"`
			WindowEnd          types.BlockHeight      `json:"windowEnd"`
			Payout             types.Currency         `json:"payout"`
			ValidProofOutputs  []encodedSiacoinOutput `json:"validProofOutputs"`
			MissedProofOutputs []encodedSiacoinOutput `json:"missedProofOutputs"`
			UnlockHash         types.UnlockHash       `json:"unlockHash"`
			RevisionNumber     uint64                 `json:"revisionNumber"`
		} `json:"fileContracts,omitempty"`
		FileContractRevisions []struct {
			ParentID              types.FileContractID    `json:"parentID"`
			UnlockConditions      encodedUnlockConditions `json:"unlockConditions"`
			NewRevisionNumber     uint64                  `json:"newRevisionNumber"`
			NewFileSize           uint64                  `json:"newFileSize"`
			NewFileMerkleRoot     crypto.Hash             `json:"newFileMerkleRoot"`
			NewWindowStart        types.BlockHeight       `json:"newWindowStart"`
			NewWindowEnd          types.BlockHeight       `json:"newWindowEnd"`
			NewValidProofOutputs  []encodedSiacoinOutput  `json:"newValidProofOutputs"`
			NewMissedProofOutputs []encodedSiacoinOutput  `json:"newMissedProofOutputs"`
			NewUnlockHash         types.UnlockHash        `json:"newUnlockHash"`
		} `json:"fileContractRevisions,omitempty"`
		StorageProofs []types.StorageProof `json:"storageProofs,omitempty"`
		SiafundInputs []struct {
			ParentID         types.SiafundOutputID   `json:"parentID"`
			UnlockConditions encodedUnlockConditions `json:"unlockConditions"`
			ClaimUnlockHash  types.UnlockHash        `json:"claimUnlockHash"`
		} `json:"siafundInputs,omitempty"`
		SiafundOutputs []struct {
			Value      types.Currency   `json:"value"`
			UnlockHash types.UnlockHash `json:"unlockHash"`
			ClaimStart types.Currency   `json:"-"` // internal, must always be 0
		} `json:"siafundOutputs,omitempty"`
		MinerFees             []types.Currency `json:"minerFees,omitempty"`
		ArbitraryData         [][]byte         `json:"arbitraryData,omitempty"`
		TransactionSignatures []struct {
			ParentID       crypto.Hash       `json:"parentID"`
			PublicKeyIndex uint64            `json:"publicKeyIndex"`
			Timelock       types.BlockHeight `json:"timelock,omitempty"`
			CoveredFields  struct {
				WholeTransaction      bool     `json:"wholeTransaction,omitempty"`
				SiacoinInputs         []uint64 `json:"siacoinInputs,omitempty"`
				SiacoinOutputs        []uint64 `json:"siacoinOutputs,omitempty"`
				FileContracts         []uint64 `json:"fileContracts,omitempty"`
				FileContractRevisions []uint64 `json:"fileContractRevisions,omitempty"`
				StorageProofs         []uint64 `json:"storageProofs,omitempty"`
				SiafundInputs         []uint64 `json:"siafundInputs,omitempty"`
				SiafundOutputs        []uint64 `json:"siafundOutputs,omitempty"`
				MinerFees             []uint64 `json:"minerFees,omitempty"`
				ArbitraryData         []uint64 `json:"arbitraryData,omitempty"`
				TransactionSignatures []uint64 `json:"transactionSignatures,omitempty"`
			} `json:"coveredFields"`
			Signature []byte `json:"signature"`
		} `json:"transactionSignatures,omitempty"`
	}
	return json.Marshal(*(*encodedTransaction)(unsafe.Pointer(&txn)))
}

type encodedSiacoinOutput struct {
	Value      types.Currency   `json:"value"`
	UnlockHash types.UnlockHash `json:"unlockHash"`
}

type encodedUnlockConditions types.UnlockConditions

func (uc encodedUnlockConditions) MarshalJSON() ([]byte, error) {
	s := struct {
		Timelock           types.BlockHeight `json:"timelock,omitempty"`
		PublicKeys         []string          `json:"publicKeys"`
		SignaturesRequired uint64            `json:"signaturesRequired"`
	}{
		Timelock:           uc.Timelock,
		PublicKeys:         make([]string, len(uc.PublicKeys)),
		SignaturesRequired: uc.SignaturesRequired,
	}
	for i := range s.PublicKeys {
		s.PublicKeys[i] = uc.PublicKeys[i].Algorithm.String() + ":" + hex.EncodeToString(uc.PublicKeys[i].Key)
	}
	return json.Marshal(s)
}

type responseAddressesAddr wallet.SeedAddressInfo

// MarshalJSON implements json.Marshaler.
func (r responseAddressesAddr) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		UnlockConditions encodedUnlockConditions `json:"unlockConditions"`
		KeyIndex         uint64                  `json:"keyIndex"`
	}{encodedUnlockConditions(r.UnlockConditions), r.KeyIndex})
}

type responseBlockRewards []wallet.BlockReward

func (r responseBlockRewards) MarshalJSON() ([]byte, error) {
	enc := make([]struct {
		ID         types.SiacoinOutputID `json:"ID"`
		Value      types.Currency        `json:"value"`
		UnlockHash types.UnlockHash      `json:"unlockHash"`
		Timelock   types.BlockHeight     `json:"timelock"`
	}, len(r))
	for i := range enc {
		enc[i].ID = r[i].ID
		enc[i].Value = r[i].Value
		enc[i].UnlockHash = r[i].UnlockHash
		enc[i].Timelock = r[i].Timelock
	}
	return json.Marshal(enc)
}

// ResponseConsensus is the response type for the /consensus endpoint.
type ResponseConsensus struct {
	Height types.BlockHeight `json:"height"`
	CCID   crypto.Hash       `json:"ccid"`
}

type responseLimbo []wallet.LimboTransaction

func (r responseLimbo) MarshalJSON() ([]byte, error) {
	// NOTE: normally we would simply embed the JSONTransaction field, but doing
	// so would cause the entire struct to inherit JSONTransaction's MarshalJSON
	// method, meaning that the ID and LimboSince fields would be ignored. There
	// is no idiomatic workaround for this; we hack around it by encoding the
	// fields separately and manually stitching them together.
	enc := make([]json.RawMessage, len(r))
	for i := range enc {
		js1, _ := json.Marshal(JSONTransaction(r[i].Transaction))
		js2, _ := json.Marshal(struct {
			ID         string    `json:"id"`
			LimboSince time.Time `json:"limboSince"`
		}{r[i].ID().String(), r[i].LimboSince})
		js2[0] = ','
		enc[i] = append(js1[:len(js1)-1], js2...)
	}
	return json.Marshal(enc)
}

type responseFileContracts []wallet.FileContract

func (r responseFileContracts) MarshalJSON() ([]byte, error) {
	enc := make([]struct {
		ID                 types.FileContractID     `json:"id"`
		FileSize           uint64                   `json:"fileSize"`
		FileMerkleRoot     crypto.Hash              `json:"fileMerkleRoot"`
		WindowStart        types.BlockHeight        `json:"windowStart"`
		WindowEnd          types.BlockHeight        `json:"windowEnd"`
		Payout             types.Currency           `json:"payout"`
		ValidProofOutputs  []encodedSiacoinOutput   `json:"validProofOutputs"`
		MissedProofOutputs []encodedSiacoinOutput   `json:"missedProofOutputs"`
		UnlockHash         types.UnlockHash         `json:"unlockHash"`
		UnlockConditions   *encodedUnlockConditions `json:"unlockConditions,omitempty"`
		RevisionNumber     uint64                   `json:"revisionNumber"`
	}, len(r))
	for i := range enc {
		enc[i].ID = r[i].ID
		enc[i].FileSize = r[i].FileSize
		enc[i].FileMerkleRoot = r[i].FileMerkleRoot
		enc[i].WindowStart = r[i].WindowStart
		enc[i].WindowEnd = r[i].WindowEnd
		enc[i].Payout = r[i].Payout
		enc[i].ValidProofOutputs = *(*[]encodedSiacoinOutput)(unsafe.Pointer(&r[i].ValidProofOutputs))
		enc[i].MissedProofOutputs = *(*[]encodedSiacoinOutput)(unsafe.Pointer(&r[i].MissedProofOutputs))
		enc[i].UnlockHash = r[i].UnlockHash
		ucs := (*encodedUnlockConditions)(&r[i].UnlockConditions)
		if len(r[i].UnlockConditions.PublicKeys) == 0 {
			ucs = nil
		}
		enc[i].UnlockConditions = ucs
		enc[i].RevisionNumber = r[i].RevisionNumber
	}
	return json.Marshal(enc)
}

// ResponseTransactionsID is the response type for the /transactions/:id
// endpoint.
type ResponseTransactionsID struct {
	Transaction types.Transaction `json:"transaction"`
	BlockID     types.BlockID     `json:"blockID"`
	BlockHeight types.BlockHeight `json:"blockHeight"`
	Timestamp   time.Time         `json:"timestamp"`
	FeePerByte  types.Currency    `json:"feePerByte"`
	Credit      types.Currency    `json:"credit"`
	Debit       types.Currency    `json:"debit"`
}

// MarshalJSON implements json.Marshaler.
func (r ResponseTransactionsID) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Transaction JSONTransaction   `json:"transaction"`
		BlockID     types.BlockID     `json:"blockID"`
		BlockHeight types.BlockHeight `json:"blockHeight"`
		Timestamp   time.Time         `json:"timestamp"`
		FeePerByte  types.Currency    `json:"feePerByte"`
		Credit      types.Currency    `json:"credit"`
		Debit       types.Currency    `json:"debit"`
	}{JSONTransaction(r.Transaction), r.BlockID, r.BlockHeight, r.Timestamp, r.FeePerByte, r.Credit, r.Debit})
}

type responseBatchqueryAddresses map[types.UnlockHash]wallet.SeedAddressInfo

// MarshalJSON implements json.Marshaler.
func (r responseBatchqueryAddresses) MarshalJSON() ([]byte, error) {
	m := make(map[string]responseAddressesAddr, len(r))
	for addr, info := range r {
		m[addr.String()] = responseAddressesAddr(info)
	}
	return json.Marshal(m)
}

// UnmarshalJSON implements json.Unmarshaler.
func (r *responseBatchqueryAddresses) UnmarshalJSON(b []byte) error {
	if *r == nil {
		*r = make(responseBatchqueryAddresses)
	}
	var m map[string]wallet.SeedAddressInfo
	err := json.Unmarshal(b, &m)
	for addr, info := range m {
		var uh types.UnlockHash
		uh.LoadString(addr)
		(*r)[uh] = info
	}
	return err
}

type responseBatchqueryTransactions map[types.TransactionID]ResponseTransactionsID

func (r responseBatchqueryTransactions) MarshalJSON() ([]byte, error) {
	m := make(map[string]ResponseTransactionsID, len(r))
	for id, txn := range r {
		m[id.String()] = txn
	}
	return json.Marshal(m)
}

func (r *responseBatchqueryTransactions) UnmarshalJSON(b []byte) error {
	if *r == nil {
		*r = make(responseBatchqueryTransactions)
	}
	var m map[string]ResponseTransactionsID
	err := json.Unmarshal(b, &m)
	for idStr, txn := range m {
		var id types.TransactionID
		(*crypto.Hash)(&id).LoadString(idStr)
		(*r)[id] = txn
	}
	return err
}
