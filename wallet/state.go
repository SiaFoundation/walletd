package wallet

import (
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
)

// A Midstate is a snapshot of unapplied consensus changes.
type Midstate struct {
	SpentSiacoinOutputs map[types.Hash256]bool
	SpentSiafundOutputs map[types.Hash256]bool

	NewSiacoinOutputs map[types.Hash256]types.SiacoinElement
	NewSiafundOutputs map[types.Hash256]types.SiafundElement

	Events []Event
}

func (ms *Midstate) Apply(cau *chain.ApplyUpdate, ownsAddress func(types.Address) bool) {
	events := AppliedEvents(cau.State, cau.Block, cau, ownsAddress)
	ms.Events = append(ms.Events, events...)

	cau.ForEachSiacoinElement(func(se types.SiacoinElement, spent bool) {
		if !ownsAddress(se.SiacoinOutput.Address) {
			return
		}

		if spent {
			ms.SpentSiacoinOutputs[se.ID] = true
			delete(ms.NewSiacoinOutputs, se.ID)
		} else {
			ms.NewSiacoinOutputs[se.ID] = se
		}
	})

	cau.ForEachSiafundElement(func(sf types.SiafundElement, spent bool) {
		if !ownsAddress(sf.SiafundOutput.Address) {
			return
		}

		if spent {
			ms.SpentSiafundOutputs[sf.ID] = true
			delete(ms.NewSiafundOutputs, sf.ID)
		} else {
			ms.NewSiafundOutputs[sf.ID] = sf
		}
	})
}

func (ms *Midstate) Revert(cru *chain.RevertUpdate, ownsAddress func(types.Address) bool) {
	revertedBlockID := cru.Block.ID()
	for i := len(ms.Events) - 1; i >= 0; i-- {
		// working backwards, revert all events until the block ID no longer
		// matches.
		if ms.Events[i].Index.ID != revertedBlockID {
			break
		}
		ms.Events = ms.Events[:i]
	}

	cru.ForEachSiacoinElement(func(se types.SiacoinElement, spent bool) {
		if !ownsAddress(se.SiacoinOutput.Address) {
			return
		}

		if !spent {
			delete(ms.SpentSiacoinOutputs, se.ID)
		}
	})

	cru.ForEachSiafundElement(func(sf types.SiafundElement, spent bool) {
		if !ownsAddress(sf.SiafundOutput.Address) {
			return
		}

		if spent {
			ms.SpentSiafundOutputs[sf.ID] = true
			delete(ms.NewSiafundOutputs, sf.ID)
		} else {
			ms.NewSiafundOutputs[sf.ID] = sf
		}
	})
}
