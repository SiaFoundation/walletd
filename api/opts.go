package api

import "net/url"

// A TxPoolOpt is an option for configuring transaction pool behavior.
type TxPoolOpt func(*url.Values)

// WithAllowVoid allows transactions that send outputs to the void address
func WithAllowVoid() TxPoolOpt {
	return func(v *url.Values) {
		v.Set("allowVoid", "true")
	}
}
