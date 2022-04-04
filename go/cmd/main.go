package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"gopkg.in/DataDog/dd-trace-go.v1/profiler"

	"github.com/carlosroman/proxy-filter/go/internal/pkg/server"
)

func main() {

	baseEndpoint := flag.String("base-endpoint", "http://127.0.0.1:8080", "The base endpoint which to proxy all requests to")
	prefix := flag.String("prefix", "", "The metric name prefix filter")

	flag.Parse()
	conf := server.Config{BaseEndpoint: *baseEndpoint, MetricsPrefixFilter: *prefix}
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
	mux.HandleFunc("/api/v1/series", handler.MetricsFilter)
	mux.HandleFunc("/", handler.ProxyHandle)

	err := profiler.Start(
		profiler.WithService("proxy-filter-go"),
		profiler.WithEnv("carlos.roman"),
		profiler.WithVersion("0.1.0"),
		profiler.WithProfileTypes(
			profiler.CPUProfile,
			profiler.HeapProfile,
			// The profiles below are disabled by default to keep overhead
			// low, but can be enabled as needed.

			// profiler.BlockProfile,
			// profiler.MutexProfile,
			// profiler.GoroutineProfile,
		),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer profiler.Stop()

	err = http.ListenAndServe(":8081", mux)
	if err != nil {
		os.Exit(-1)
	}
}
