package api

import (
	"encoding/hex"
	"strconv"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/walletd/internal/prometheus"
	"go.sia.tech/walletd/wallet"
)

type ConsensusTipResp consensus.State

// PrometheusMetric returns Prometheus metrics for /consensus/tip endpoint
func (c ConsensusTipResp) PrometheusMetric() (metrics []prometheus.Metric) {
	return []prometheus.Metric{
		{
			Name:  "walletd_consensus_tip_height",
			Value: float64(c.Index.Height),
		},
	}
}

type SyncerPeersResp []GatewayPeer

// PrometheusMetric returns Prometheus metrics for /syncer/peers endpoint
func (p SyncerPeersResp) PrometheusMetric() (metrics []prometheus.Metric) {
	for _, peer := range p {
		metrics = append(metrics, prometheus.Metric{
			Name: "walletd_syncer_peer",
			Labels: map[string]any{
				"address": string(peer.Addr),
				"version": peer.Version,
			},
			Value: 1,
		})
	}
	return
}

type NetworkResp consensus.State

// PrometheusMetric returns Prometheus metrics for /consensus/network endpoint
func (n NetworkResp) PrometheusMetric() (metrics []prometheus.Metric) {
	return []prometheus.Metric{
		{
			Name: "walletd_consensus_network",
			Labels: map[string]any{
				"network": n.Network.Name,
			},
			Value: 1,
		},
	}
}

// PrometheusMetric returns Prometheus metrics for /txpool/transactions endpoint
func (t TxpoolTransactionsResponse) PrometheusMetric() (metrics []prometheus.Metric) {
	return []prometheus.Metric{
		{
			Name:  "walletd_txpool_numtxns_v1",
			Value: float64(len(t.Transactions)),
		},
		{
			Name:  "walletd_txpool_numtxns_v2",
			Value: float64(len(t.V2Transactions)),
		},
	}
}

type TPoolResp types.Currency

// PrometheusMetric returns Prometheus metrics for /txpool/fee endpoint
func (t TPoolResp) PrometheusMetric() (metrics []prometheus.Metric) {
	return []prometheus.Metric{
		{
			Name:  "walletd_txpool_fee",
			Value: types.Currency(t).Siacoins(),
		},
	}
}

// PrometheusMetric returns Prometheus metrics for /wallets/:name/balance endpoint
func (t BalanceResponse) PrometheusMetric() (metrics []prometheus.Metric) {
	return []prometheus.Metric{
		{
			Name: "walletd_wallet_balance_siacoins",
			Labels: map[string]any{
				"id": t.ID,
			},
			Value: t.Balance.Siacoins.Siacoins(),
		},
		{
			Name: "walletd_wallet_balance_siafunds",
			Labels: map[string]any{
				"id": t.ID,
			},
			Value: float64(t.Balance.Siafunds),
		},
	}
}

type WalletEventResp struct {
	ID     wallet.ID
	Events []wallet.Event
}

// PrometheusMetric returns Prometheus metrics for /wallets/:name/events endpoint
func (t WalletEventResp) PrometheusMetric() (metrics []prometheus.Metric) {
	for _, event := range t.Events {
		labels := make(map[string]interface{}, 4)
		labels["id"] = t.ID
		labels["height"] = event.Index.Height
		labels["timestamp"] = strconv.FormatInt(event.Timestamp.UnixMilli(), 10)
		if e, ok := event.Data.(*wallet.EventTransaction); ok {
			var txvalue float64
			var inflow types.Currency
			for _, si := range e.SiacoinInputs {
				inflow.Add(si.SiacoinOutput.Value)
			}
			var outflow types.Currency
			for _, si := range e.SiacoinOutputs {
				inflow.Add(si.SiacoinOutput.Value)
			}
			if inflow.Cmp(outflow) > 0 { // inflow > outflow = positive value
				txvalue = inflow.Sub(outflow).Siacoins()
			} else { // inflow < outflow = negative value
				txvalue = outflow.Sub(inflow).Siacoins() * -1
			}
			labels["txid"] = hex.EncodeToString(event.ID[:])
			return []prometheus.Metric{
				{
					Name:   "walletd_wallet_transaction",
					Labels: labels,
					Value:  txvalue,
				},
			}
		} else if e, ok := event.Data.(*wallet.EventMinerPayout); ok {
			return []prometheus.Metric{
				{
					Name:   "walletd_wallet_event_minerpayout",
					Labels: labels,
					Value:  e.SiacoinOutput.SiacoinOutput.Value.Siacoins(),
				},
			}
		} else if _, ok := event.Data.(*wallet.EventFoundationSubsidy); ok {
			return []prometheus.Metric{
				{
					Name:   "walletd_wallet_event_foundationsubsidy",
					Labels: labels,
					Value:  e.SiacoinOutput.SiacoinOutput.Value.Siacoins(),
				},
			}
		} else if _, ok := event.Data.(*wallet.EventContractPayout); ok {

			return []prometheus.Metric{
				{
					Name:   "walletd_wallet_event_contractpayout",
					Labels: labels,
					Value:  e.SiacoinOutput.SiacoinOutput.Value.Siacoins(),
				},
			}
		}
	}
	return
}
