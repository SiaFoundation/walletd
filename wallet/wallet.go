package wallet

import (
	"encoding/json"
	"fmt"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
)

// A SiacoinElement is a siacoin output paired with its ID.
type SiacoinElement struct {
	ID types.SiacoinOutputID
	types.SiacoinOutput
}

// A SiafundElement is a siafund output paired with its ID.
type SiafundElement struct {
	ID types.SiafundOutputID
	types.SiafundOutput
}

// StandardTransactionSignature is the most common form of TransactionSignature.
// It covers the entire transaction, references a sole public key, and has no
// timelock.
func StandardTransactionSignature(id types.Hash256) types.TransactionSignature {
	return types.TransactionSignature{
		ParentID:       id,
		CoveredFields:  types.CoveredFields{WholeTransaction: true},
		PublicKeyIndex: 0,
	}
}

// SignTransaction signs txn with the given key. The TransactionSignature object
// must already be present in txn at the given index.
func SignTransaction(cs consensus.State, txn *types.Transaction, sigIndex int, key types.PrivateKey) {
	tsig := &txn.Signatures[sigIndex]
	var sigHash types.Hash256
	if tsig.CoveredFields.WholeTransaction {
		sigHash = cs.WholeSigHash(*txn, tsig.ParentID, tsig.PublicKeyIndex, tsig.Timelock, tsig.CoveredFields.Signatures)
	} else {
		sigHash = cs.PartialSigHash(*txn, tsig.CoveredFields)
	}
	sig := key.SignHash(sigHash)
	tsig.Signature = sig[:]
}

// A PoolTransaction summarizes the wallet-relevant data in a txpool
// transaction.
type PoolTransaction struct {
	ID       types.TransactionID
	Raw      types.Transaction
	Type     string
	Sent     types.Currency
	Received types.Currency
	Locked   types.Currency
}

// Annotate annotates a txpool transaction.
func Annotate(txn types.Transaction, ownsAddress func(types.Address) bool) PoolTransaction {
	ptxn := PoolTransaction{ID: txn.ID(), Raw: txn, Type: "unknown"}

	var totalValue types.Currency
	for _, sco := range txn.SiacoinOutputs {
		totalValue = totalValue.Add(sco.Value)
	}
	for _, fc := range txn.FileContracts {
		totalValue = totalValue.Add(fc.Payout)
	}
	for _, fee := range txn.MinerFees {
		totalValue = totalValue.Add(fee)
	}

	var ownedIn, ownedOut int
	for _, sci := range txn.SiacoinInputs {
		if ownsAddress(sci.UnlockConditions.UnlockHash()) {
			ownedIn++
		}
	}
	for _, sco := range txn.SiacoinOutputs {
		if ownsAddress(sco.Address) {
			ownedOut++
		}
	}
	var ins, outs string
	switch {
	case ownedIn == 0:
		ins = "none"
	case ownedIn < len(txn.SiacoinInputs):
		ins = "some"
	case ownedIn == len(txn.SiacoinInputs):
		ins = "all"
	}
	switch {
	case ownedOut == 0:
		ins = "none"
	case ownedOut < len(txn.SiacoinOutputs):
		ins = "some"
	case ownedOut == len(txn.SiacoinOutputs):
		ins = "all"
	}

	switch {
	case ins == "none" && outs == "none":
		ptxn.Type = "unrelated"
	case ins == "all":
		ptxn.Sent = totalValue
		switch {
		case outs == "all":
			ptxn.Type = "redistribution"
		case len(txn.FileContractRevisions) > 0:
			ptxn.Type = "contract revision"
		case len(txn.StorageProofs) > 0:
			ptxn.Type = "storage proof"
		case len(txn.ArbitraryData) > 0:
			ptxn.Type = "announcement"
		default:
			ptxn.Type = "send"
		}
	case ins == "none" && outs != "none":
		ptxn.Type = "receive"
		for _, sco := range txn.SiacoinOutputs {
			if ownsAddress(sco.Address) {
				ptxn.Received = ptxn.Received.Add(sco.Value)
			}
		}
	case ins == "some" && len(txn.FileContracts) > 0:
		ptxn.Type = "contract"
		for _, fc := range txn.FileContracts {
			var validLocked, missedLocked types.Currency
			for _, sco := range fc.ValidProofOutputs {
				if ownsAddress(sco.Address) {
					validLocked = validLocked.Add(fc.Payout)
				}
			}
			for _, sco := range fc.MissedProofOutputs {
				if ownsAddress(sco.Address) {
					missedLocked = missedLocked.Add(fc.Payout)
				}
			}
			if validLocked.Cmp(missedLocked) > 0 {
				ptxn.Locked = ptxn.Locked.Add(validLocked)
			} else {
				ptxn.Locked = ptxn.Locked.Add(missedLocked)
			}
		}
	}

	return ptxn
}

// An Event is something interesting that happened on the Sia blockchain.
type Event struct {
	Timestamp time.Time
	Index     types.ChainIndex
	Val       interface{ eventType() string }
}

// MarshalJSON implements json.Marshaler.
func (e Event) MarshalJSON() ([]byte, error) {
	val, _ := json.Marshal(e.Val)
	return json.Marshal(struct {
		Timestamp time.Time
		Index     types.ChainIndex
		Type      string
		Val       json.RawMessage
	}{
		Timestamp: e.Timestamp,
		Index:     e.Index,
		Type:      e.Val.eventType(),
		Val:       val,
	})
}

// UnmarshalJSON implements json.Unarshaler.
func (e *Event) UnmarshalJSON(data []byte) error {
	var s struct {
		Timestamp time.Time
		Index     types.ChainIndex
		Type      string
		Val       json.RawMessage
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	e.Timestamp = s.Timestamp
	e.Index = s.Index
	switch s.Type {
	case (EventBlockReward{}).eventType():
		e.Val = new(EventBlockReward)
	case (EventFoundationSubsidy{}).eventType():
		e.Val = new(EventFoundationSubsidy)
	case (EventSiacoinMaturation{}).eventType():
		e.Val = new(EventSiacoinMaturation)
	case (EventSiacoinTransfer{}).eventType():
		e.Val = new(EventSiacoinTransfer)
	case (EventSiafundTransfer{}).eventType():
		e.Val = new(EventSiafundTransfer)
	case (EventFileContractFormation{}).eventType():
		e.Val = new(EventFileContractFormation)
	case (EventFileContractRevision{}).eventType():
		e.Val = new(EventFileContractRevision)
	case (EventFileContractResolutionValid{}).eventType():
		e.Val = new(EventFileContractResolutionValid)
	case (EventFileContractResolutionMissed{}).eventType():
		e.Val = new(EventFileContractResolutionMissed)
	case (EventHostAnnouncement{}).eventType():
		e.Val = new(EventHostAnnouncement)
	case (EventTransaction{}).eventType():
		e.Val = new(EventTransaction)
	}
	if e.Val == nil {
		return fmt.Errorf("unknown event type %q", s.Type)
	}
	return json.Unmarshal(s.Val, e.Val)
}

func (EventBlockReward) eventType() string                 { return "block reward" }
func (EventFoundationSubsidy) eventType() string           { return "foundation subsidy" }
func (EventSiacoinMaturation) eventType() string           { return "siacoin maturation" }
func (EventSiacoinTransfer) eventType() string             { return "siacoin transfer" }
func (EventSiafundTransfer) eventType() string             { return "siafund transfer" }
func (EventFileContractFormation) eventType() string       { return "file contract formation" }
func (EventFileContractRevision) eventType() string        { return "file contract revision" }
func (EventFileContractResolutionValid) eventType() string { return "file contract resolution (valid)" }
func (EventFileContractResolutionMissed) eventType() string {
	return "file contract resolution (missed)"
}
func (EventHostAnnouncement) eventType() string { return "host announcement" }
func (EventTransaction) eventType() string      { return "transaction" }

// EventBlockReward represents a block reward.
type EventBlockReward struct {
	OutputID       types.SiacoinOutputID
	Output         types.SiacoinOutput
	MaturityHeight uint64
}

// EventFoundationSubsidy represents a Foundation subsidy.
type EventFoundationSubsidy struct {
	OutputID       types.SiacoinOutputID
	Output         types.SiacoinOutput
	MaturityHeight uint64
}

// EventSiacoinMaturation represents the maturation of a siacoin output.
type EventSiacoinMaturation struct {
	OutputID types.SiacoinOutputID
	Output   types.SiacoinOutput
	Source   consensus.DelayedOutputSource
}

// EventSiacoinTransfer represents the transfer of siacoins within a
// transaction.
type EventSiacoinTransfer struct {
	TransactionID types.TransactionID
	Inputs        []SiacoinElement
	Outputs       []SiacoinElement
	Fee           types.Currency
}

// EventSiafundTransfer represents the transfer of siafunds within a
// transaction.
type EventSiafundTransfer struct {
	TransactionID types.TransactionID
	Inputs        []SiafundElement
	Outputs       []SiafundElement
	ClaimOutputID types.SiacoinOutputID
	ClaimOutput   types.SiacoinOutput
}

// EventFileContractFormation represents the formation of a file contract within
// a transaction.
type EventFileContractFormation struct {
	TransactionID types.TransactionID
	ContractID    types.FileContractID
	Contract      types.FileContract
}

// EventFileContractRevision represents the revision of a file contract within
// a transaction.
type EventFileContractRevision struct {
	TransactionID types.TransactionID
	ContractID    types.FileContractID
	OldContract   types.FileContract
	NewContract   types.FileContract
}

// EventFileContractResolutionValid represents the valid resolution of a file
// contract within a transaction.
type EventFileContractResolutionValid struct {
	TransactionID  types.TransactionID
	ContractID     types.FileContractID
	Contract       types.FileContract
	OutputID       types.SiacoinOutputID
	Output         types.SiacoinOutput
	MaturityHeight uint64
}

// EventFileContractResolutionMissed represents the expiration of a file
// contract.
type EventFileContractResolutionMissed struct {
	Contract       types.FileContract
	OutputID       types.SiacoinOutputID
	Output         types.SiacoinOutput
	MaturityHeight uint64
}

// EventHostAnnouncement represents a host announcement within a transaction.
type EventHostAnnouncement struct {
	TransactionID types.TransactionID
	PublicKey     types.PublicKey
	NetAddress    string
	Inputs        []SiacoinElement
}

// EventTransaction represents a generic transaction.
type EventTransaction struct {
	TransactionID types.TransactionID
	Transaction   types.Transaction
}

// DiffEvents extracts a list of events from a block diff.
func DiffEvents(b types.Block, diff consensus.BlockDiff, index types.ChainIndex, relevant func(types.Address) bool) []Event {
	var events []Event
	addEvent := func(v interface{ eventType() string }) {
		events = append(events, Event{
			Timestamp: b.Timestamp,
			Index:     index,
			Val:       v,
		})
	}

	relevantContract := func(fc types.FileContract) bool {
		for _, sco := range fc.ValidProofOutputs {
			if relevant(sco.Address) {
				return true
			}
		}
		for _, sco := range fc.MissedProofOutputs {
			if relevant(sco.Address) {
				return true
			}
		}
		return false
	}
	relevantTxn := func(tdiff consensus.TransactionDiff) bool {
		for _, scod := range tdiff.CreatedSiacoinOutputs {
			if relevant(scod.Output.Address) {
				return true
			}
		}
		for _, dscod := range tdiff.ImmatureSiacoinOutputs {
			if relevant(dscod.Output.Address) {
				return true
			}
		}
		for _, sfod := range tdiff.CreatedSiafundOutputs {
			if relevant(sfod.Output.Address) {
				return true
			}
		}
		for _, fcd := range tdiff.CreatedFileContracts {
			if relevantContract(fcd.Contract) {
				return true
			}
		}
		for _, scod := range tdiff.SpentSiacoinOutputs {
			if relevant(scod.Output.Address) {
				return true
			}
		}
		for _, sfod := range tdiff.SpentSiafundOutputs {
			if relevant(sfod.Output.Address) {
				return true
			}
		}
		for _, fcd := range tdiff.RevisedFileContracts {
			if relevantContract(fcd.NewContract) {
				return true
			}
		}
		for _, fcd := range tdiff.ValidFileContracts {
			if relevantContract(fcd.Contract) {
				return true
			}
		}
		return false
	}

	for i, tdiff := range diff.Transactions {
		if !relevantTxn(tdiff) {
			continue
		}
		txn := b.Transactions[i]
		txid := txn.ID()
		oldLen := len(events)

		var scInputs, scOutputs []SiacoinElement
		for _, scod := range tdiff.SpentSiacoinOutputs {
			if relevant(scod.Output.Address) {
				scInputs = append(scInputs, SiacoinElement{
					ID:            scod.ID,
					SiacoinOutput: scod.Output,
				})
			}
		}
		for _, scod := range tdiff.CreatedSiacoinOutputs {
			if relevant(scod.Output.Address) {
				scOutputs = append(scOutputs, SiacoinElement{
					ID:            scod.ID,
					SiacoinOutput: scod.Output,
				})
			}
		}
		var fee types.Currency
		for _, c := range txn.MinerFees {
			fee = fee.Add(c)
		}
		if len(scInputs) > 0 || len(scOutputs) > 0 {
			addEvent(EventSiacoinTransfer{
				TransactionID: txid,
				Inputs:        scInputs,
				Outputs:       scOutputs,
				Fee:           fee,
			})
		}

		var sfInputs, sfOutputs []SiafundElement
		for _, sfod := range tdiff.SpentSiafundOutputs {
			if relevant(sfod.Output.Address) {
				sfInputs = append(sfInputs, SiafundElement{
					ID:            sfod.ID,
					SiafundOutput: sfod.Output,
				})
			}
		}
		for _, sfod := range tdiff.CreatedSiafundOutputs {
			if relevant(sfod.Output.Address) {
				sfOutputs = append(sfOutputs, SiafundElement{
					ID:            sfod.ID,
					SiafundOutput: sfod.Output,
				})
			}
		}
		if len(sfInputs) > 0 || len(sfOutputs) > 0 {
			addEvent(EventSiafundTransfer{
				TransactionID: txid,
				Inputs:        sfInputs,
				Outputs:       sfOutputs,
			})
		}

		for _, fcd := range tdiff.CreatedFileContracts {
			if relevantContract(fcd.Contract) {
				addEvent(EventFileContractFormation{
					TransactionID: txid,
					ContractID:    fcd.ID,
					Contract:      fcd.Contract,
				})
			}
		}
		for _, fcrd := range tdiff.RevisedFileContracts {
			if relevantContract(fcrd.OldContract) || relevantContract(fcrd.NewContract) {
				addEvent(EventFileContractRevision{
					TransactionID: txid,
					ContractID:    fcrd.ID,
					OldContract:   fcrd.OldContract,
					NewContract:   fcrd.NewContract,
				})
			}
		}
		for _, fcd := range tdiff.ValidFileContracts {
			if relevantContract(fcd.Contract) {
				addEvent(EventFileContractResolutionValid{
					TransactionID: txid,
					ContractID:    fcd.ID,
					Contract:      fcd.Contract,
				})
			}
		}

		// only report host announcements that we paid for
		if len(scInputs) > 0 {
			decodeAnnouncement := func(b []byte) (types.PublicKey, string, bool) {
				var prefix types.Specifier
				var uk types.UnlockKey
				d := types.NewBufDecoder(b)
				prefix.DecodeFrom(d)
				netAddress := d.ReadString()
				uk.DecodeFrom(d)
				if d.Err() != nil ||
					prefix != types.NewSpecifier("HostAnnouncement") ||
					uk.Algorithm != types.SpecifierEd25519 ||
					len(uk.Key) != len(types.PublicKey{}) {
					return types.PublicKey{}, "", false
				}
				return *(*types.PublicKey)(uk.Key), netAddress, true
			}
			for _, arb := range txn.ArbitraryData {
				if pubkey, netAddress, ok := decodeAnnouncement(arb); ok {
					addEvent(EventHostAnnouncement{
						TransactionID: txid,
						PublicKey:     pubkey,
						NetAddress:    netAddress,
						Inputs:        scInputs,
					})
				}
			}
		}

		if len(events) == oldLen {
			// transaction is relevant, but doesn't map to any predefined
			// event categories
			addEvent(EventTransaction{
				TransactionID: txid,
				Transaction:   txn,
			})
		}
	}

	for _, dscod := range diff.MaturedSiacoinOutputs {
		if relevant(dscod.Output.Address) {
			addEvent(EventSiacoinMaturation{
				OutputID: dscod.ID,
				Output:   dscod.Output,
				Source:   dscod.Source,
			})
		}
	}
	for _, dscod := range diff.ImmatureSiacoinOutputs {
		if relevant(dscod.Output.Address) {
			switch dscod.Source {
			case consensus.OutputSourceMiner:
				addEvent(EventBlockReward{
					OutputID:       dscod.ID,
					Output:         dscod.Output,
					MaturityHeight: dscod.MaturityHeight,
				})
			case consensus.OutputSourceMissedContract:
				addEvent(EventFileContractResolutionMissed{
					OutputID:       dscod.ID,
					Output:         dscod.Output,
					MaturityHeight: dscod.MaturityHeight,
				})
			case consensus.OutputSourceFoundation:
				addEvent(EventBlockReward{
					OutputID:       dscod.ID,
					Output:         dscod.Output,
					MaturityHeight: dscod.MaturityHeight,
				})
			}
		}
	}
	for _, fcd := range diff.MissedFileContracts {
		for i, sco := range fcd.Contract.MissedProofOutputs {
			if relevant(sco.Address) {
				addEvent(EventFileContractResolutionMissed{
					Contract:       fcd.Contract,
					OutputID:       fcd.ID.MissedOutputID(i),
					Output:         sco,
					MaturityHeight: diff.ImmatureSiacoinOutputs[0].MaturityHeight,
				})
			}
		}
	}
	return events
}
