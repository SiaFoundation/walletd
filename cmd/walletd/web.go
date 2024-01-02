package main

import (
	"net"
	"net/http"
	"strings"

	"go.sia.tech/jape"
	"go.sia.tech/walletd/api"
	"go.sia.tech/web/walletd"
)

func startWeb(l net.Listener, node *node, password string, urlPrefix string) error {
	var handler http.Handler
	if urlPrefix == "/api" {
		handler = api.NewServer(node.cm, node.s, node.wm)
	} else if urlPrefix == "/prometheus" {
		handler = api.NewPrometheusServer(node.cm, node.s, node.wm)
	}

	api := jape.BasicAuth(password)(handler)
	web := walletd.Handler()
	return http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, urlPrefix) {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, urlPrefix)
			api.ServeHTTP(w, r)
			return
		}
		web.ServeHTTP(w, r)
	}))
}
