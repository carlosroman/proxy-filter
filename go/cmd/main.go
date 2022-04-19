package main

import (
	"context"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/DataDog/datadog-go/v5/statsd"
	"gopkg.in/DataDog/dd-trace-go.v1/profiler"
	"k8s.io/klog/v2"

	"github.com/carlosroman/proxy-filter/go/internal/pkg/server"
)

func main() {

	baseEndpoint := flag.String("base-endpoint", "http://127.0.0.1:8080", "The base endpoint which to proxy all requests to")
	prefix := flag.String("prefix", "", "The metric name prefix filter")
	env := flag.String("env", "dev", "The environment the proxy filter runs in")
	statsdAddr := flag.String("stats-addr", "127.0.0.1:8125", "Address for DogStatsD endpoint")
	listenAddr := flag.String("listen-addr", ":8081", "Address for proxy to listen on")
	enableProtobufFilter := flag.Bool("protobuf-filter", true, "Enable filtering of protobuf payloads")
	enableJsonFilter := flag.Bool("json-filter", true, "Enable filtering of json payloads")

	klog.InitFlags(nil)
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

	statsDClient, err := statsd.New(*statsdAddr)
	if err != nil {
		klog.Fatalf("Failed to start statsd client: %v", err)
	}

	handler := server.NewHandler(conf, httpClient, statsDClient)
	mux := http.NewServeMux()

	if *enableJsonFilter {
		mux.HandleFunc("/api/v1/series", handler.MetricsFilter)
	}

	if *enableProtobufFilter {
		mux.HandleFunc("/api/v2/series", handler.MetricsProtobufFilter)
	}

	mux.HandleFunc("/", handler.ProxyHandle)

	err = profiler.Start(
		profiler.WithService("proxy-filter-go"),
		profiler.WithEnv(*env),
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
		klog.Fatalf("Failed to start profiler: %v", err)
	}
	defer profiler.Stop()

	httpServer := &http.Server{Addr: *listenAddr, Handler: mux}
	go func(hs *http.Server) {
		klog.InfoS("Starting server", "addr", hs.Addr)
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.ErrorS(err, "Something went wrong")
			os.Exit(-1)
		}
	}(httpServer)

	cs := make(chan os.Signal, 1)
	signal.Notify(cs, os.Interrupt)
	<-cs
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	klog.Info("Attempting to shutdown")
	if err = httpServer.Shutdown(ctx); err != nil {
		klog.ErrorS(err, "Failed to shutdown server")
		os.Exit(-2)
	}
	klog.Info("Shutdown complete")
	os.Exit(0)
}
