package server

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"k8s.io/klog/v2"

	"github.com/DataDog/datadog-api-client-go/api/v1/datadog"
)

const (
	metricsFilteredCountName = "proxy_filter.filtered_metrics.count"
)

type Config struct {
	BaseEndpoint        string
	MetricsPrefixFilter string
	Tags                []string
}

func NewHandler(cfg Config, httpClient *http.Client, statsDClient statsdClient) Handler {
	return Handler{cfg: cfg, httpClient: httpClient, statsDClient: statsDClient}
}

type Handler struct {
	cfg          Config
	httpClient   *http.Client
	statsDClient statsdClient
}

func (h *Handler) ProxyHandle(w http.ResponseWriter, r *http.Request) {
	body := r.Body
	h.proxyRequest(w, r, body)
}

func (h *Handler) proxyRequest(w http.ResponseWriter, r *http.Request, body io.ReadCloser) {
	url := h.cfg.BaseEndpoint + r.URL.Path
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, body)
	req.URL.RawQuery = r.URL.RawQuery
	if err != nil {
		klog.ErrorS(err, "Got an error creating new request")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "%v", err)
		return
	}

	for key := range r.Header {
		req.Header.Add(key, r.Header.Get(key))
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		klog.ErrorS(err, "Got an error doing http request")
		w.WriteHeader(http.StatusBadGateway)
		return
	}

	defer resp.Body.Close()
	for key := range resp.Header {
		w.Header().Add(key, resp.Header.Get(key))
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	klog.InfoS("Request handled",
		"url", url,
		"Content-Encoding", r.Header.Get("Content-Encoding"),
		"Content-Type", r.Header.Get("Content-Type"),
		"Method", r.Method,
		"StatusCode", resp.StatusCode,
	)
}

func (h *Handler) MetricsFilter(w http.ResponseWriter, r *http.Request) {
	if h.cfg.MetricsPrefixFilter == "" {
		h.proxyRequest(w, r, r.Body)
		return
	}
	var payload datadog.MetricsPayload
	var err error
	var rc io.ReadCloser
	switch r.Header.Get("Content-Encoding") {
	case "gzip":
		rc, err = gzip.NewReader(r.Body)
	case "deflate":
		rc, err = zlib.NewReader(r.Body)
	default:
		rc = r.Body
	}

	if err != nil {
		klog.ErrorS(err, "Could not read body")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "%v", err)
		return
	}

	err = json.NewDecoder(rc).Decode(&payload)
	_ = rc.Close()
	if err != nil {
		klog.ErrorS(err, "Could not decode json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "%v", err)
		return
	}

	filteredSeries := make([]datadog.Series, 0, len(payload.Series))
	for i := range payload.Series {
		if !strings.HasPrefix(payload.Series[i].Metric, h.cfg.MetricsPrefixFilter) {
			filteredSeries = append(filteredSeries, payload.Series[i])
		}
	}
	_ = h.statsDClient.Count(metricsFilteredCountName, int64(len(payload.Series)-len(filteredSeries)), h.cfg.Tags, 1)
	payload.SetSeries(filteredSeries)

	buf := new(bytes.Buffer)
	var rw io.WriteCloser
	switch r.Header.Get("Content-Encoding") {
	case "gzip":
		rw = gzip.NewWriter(buf)
	case "deflate":
		rw = zlib.NewWriter(buf)
	default:
		rw = &nopWriterCloser{buf}
	}

	err = json.NewEncoder(rw).Encode(payload)
	_ = rw.Close()

	if err != nil {
		klog.ErrorS(err, "Could not encode json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "%v", err)
		return
	}
	h.proxyRequest(w, r, io.NopCloser(buf))
}

type nopWriterCloser struct {
	io.Writer
}

func (n *nopWriterCloser) Close() error {
	return nil
}

type statsdClient interface {
	Count(name string, value int64, tags []string, rate float64) error
}
