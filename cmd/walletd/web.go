package main

import (
	"net"
	"net/http"
	"strings"

	"go.sia.tech/jape"
	"go.sia.tech/walletd/api"
	"go.sia.tech/web/walletd"
)

func startWeb(l net.Listener, node *node, password string) error {
	renter := api.NewServer(node.cm, node.s, node.wm)
	api := jape.BasicAuth(password)(renter)
	web := walletd.Handler()
	return http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api") {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
			api.ServeHTTP(w, r)
			return
		}
		web.ServeHTTP(w, r)
	}))
}
