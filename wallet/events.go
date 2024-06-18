package wallet

import (
	"encoding/json"
	"fmt"
	"time"

	"go.sia.tech/core/types"
)

// event types indicate the source of an event. Events can
// either be created by sending Siacoins between addresses or they can be
// created by consensus (e.g. a miner payout, a siafund claim, or a contract).
const (
	EventTypeMinerPayout       = "miner"
	EventTypeFoundationSubsidy = "foundation"

	EventTypeV1Transaction        = "v1Transaction"
	EventTypeV1ContractResolution = "v1ContractResolution"

	EventTypeV2Transaction        = "v2Transaction"
	EventTypeV2ContractResolution = "v2ContractResolution"

	EventTypeSiafundClaim = "siafundClaim"
)

type (
	// EventData provides type safety for the Data field of an Event.
	EventData interface {
		isEvent() bool
	}

	// An Event is something interesting that happened on the Sia blockchain.
	Event struct {
		ID             types.Hash256    `json:"id"`
		Index          types.ChainIndex `json:"index"`
		Timestamp      time.Time        `json:"timestamp"`
		MaturityHeight uint64           `json:"maturityHeight"`
		Type           string           `json:"type"`
		Data           EventData        `json:"data"`
		Relevant       []types.Address  `json:"relevant,omitempty"`
	}

	// An EventV1Transaction pairs a v1 transaction with its spent siacoin and
	// siafund elements.
	EventV1Transaction struct {
		Transaction types.Transaction `json:"transaction"`
		// v1 siacoin inputs do not describe the value of the spent utxo
		SpentSiacoinElements []types.SiacoinElement `json:"spentSiacoinElements"`
		// v1 siafund inputs do not describe the value of the spent utxo
		SpentSiafundElements []types.SiafundElement `json:"spentSiafundElements"`
	}

	// An EventV2Transaction is a v2 transaction.
	EventV2Transaction types.V2Transaction

	// An EventPayout represents a payout from a siafund claim, a miner, or the
	// foundation subsidy.
	EventPayout struct {
		SiacoinElement types.SiacoinElement `json:"siacoinElement"`
	}

	// An EventV1ContractResolution represents a file contract payout from a v1
	// contract.
	EventV1ContractResolution struct {
		Parent         types.FileContractElement `json:"parent"`
		SiacoinElement types.SiacoinElement      `json:"siacoinElement"`
		Missed         bool                      `json:"missed"`
	}

	// An EventV2ContractResolution represents a file contract payout from a v2
	// contract.
	EventV2ContractResolution struct {
		types.V2FileContractResolution
		SiacoinElement types.SiacoinElement `json:"siacoinElement"`
		Missed         bool                 `json:"missed"`
	}
)

func (EventPayout) isEvent() bool               { return true }
func (EventV1ContractResolution) isEvent() bool { return true }
func (EventV2ContractResolution) isEvent() bool { return true }
func (EventV1Transaction) isEvent() bool        { return true }
func (EventV2Transaction) isEvent() bool        { return true }

// UnmarshalJSON implements the json.Unmarshaler interface.
func (e *Event) UnmarshalJSON(b []byte) error {
	var je struct {
		ID             types.Hash256    `json:"id"`
		Index          types.ChainIndex `json:"index"`
		Timestamp      time.Time        `json:"timestamp"`
		MaturityHeight uint64           `json:"maturityHeight"`
		Type           string           `json:"type"`
		Data           json.RawMessage  `json:"data"`
		Relevant       []types.Address  `json:"relevant,omitempty"`
	}
	if err := json.Unmarshal(b, &je); err != nil {
		return err
	}

	e.ID = je.ID
	e.Index = je.Index
	e.Timestamp = je.Timestamp
	e.MaturityHeight = je.MaturityHeight
	e.Type = je.Type
	e.Relevant = je.Relevant

	var err error
	switch je.Type {
	case EventTypeMinerPayout, EventTypeFoundationSubsidy, EventTypeSiafundClaim:
		var data EventPayout
		err = json.Unmarshal(je.Data, &data)
		e.Data = data
	case EventTypeV1ContractResolution:
		var data EventV1ContractResolution
		err = json.Unmarshal(je.Data, &data)
		e.Data = data
	case EventTypeV2ContractResolution:
		var data EventV2ContractResolution
		err = json.Unmarshal(je.Data, &data)
		e.Data = data
	case EventTypeV1Transaction:
		var data EventV1Transaction
		err = json.Unmarshal(je.Data, &data)
		e.Data = data
	case EventTypeV2Transaction:
		var data EventV2Transaction
		err = json.Unmarshal(je.Data, &data)
		e.Data = data
	default:
		return fmt.Errorf("unknown event type: %v", je.Type)
	}
	return err
}
