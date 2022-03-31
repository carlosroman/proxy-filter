package main

import (
	"flag"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/carlosroman/proxy-filter/go/internal/pkg/server"
)

func main() {

	baseEndpoint := flag.String("base-endpoint", "http://127.0.0.1:8080", "The base endpoint which to proxy all requests to")
	flag.Parse()
	conf := server.Config{BaseEndpoint: *baseEndpoint}
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   90 * time.Second,
				KeepAlive: 90 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxConnsPerHost:       100,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: 10 * time.Second,
		},
		Timeout: 60 * time.Second,
	}
	handler := server.NewHandler(conf, httpClient)
	mux := http.NewServeMux()
	mux.HandleFunc("/", handler.ProxyHandle)

	err := http.ListenAndServe(":8081", mux)
	if err != nil {
		os.Exit(-1)
	}
}
