package server

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/DataDog/datadog-api-client-go/api/v1/datadog"
	"github.com/richardartoul/molecule"
	"github.com/richardartoul/molecule/src/codec"
	"k8s.io/klog/v2"
)

const (
	metricsFilteredCountName = "proxy_filter.filtered_metrics.count"
	maxHTTPResponseReadBytes = 64 * 1024

	encodingGzip    = "gzip"
	encodingDeflate = "deflate"

	// constants for the protobuf data we will be filtering, taken from MetricPayload in
	// https://github.com/DataDog/agent-payload/blob/master/proto/metrics/agent_payload.proto
	metricSeries           = 1
	metricSeriesMetricName = 2
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

	defer func() {
		// Discard any remaining response body when we are done reading.
		_, _ = io.CopyN(ioutil.Discard, resp.Body, maxHTTPResponseReadBytes)
		_ = resp.Body.Close()
	}()

	for key := range resp.Header {
		w.Header().Add(key, resp.Header.Get(key))
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	klog.InfoS("Request handled",
		"url", url,
		"request_content_length", r.ContentLength,
		"content_length", r.Header.Get("Content-Encoding"),
		"content_type", r.Header.Get("Content-Type"),
		"method", r.Method,
		"status_code", resp.StatusCode,
	)
}

func (h *Handler) MetricsProtobufFilter(w http.ResponseWriter, r *http.Request) {
	if h.cfg.MetricsPrefixFilter == "" {
		h.proxyRequest(w, r, r.Body)
		return
	}

	err, rc := getReaderFromRequest(r)
	if err != nil {
		logCouldNotReadBodyError(w, err)
		return
	}

	all, err := io.ReadAll(rc)
	if err != nil {
		logCouldNotBufferBodyError(w, err)
		return
	}

	output := bytes.NewBuffer([]byte{})
	ps := molecule.NewProtoStream(output)
	buffer := codec.NewBuffer(all)
	dropCount := int64(0)
	err = molecule.MessageEach(buffer, func(fieldNum int32, value molecule.Value) (cont bool, err error) {
		switch fieldNum {
		case metricSeries:
			var packedArr []byte
			packedArr, err = value.AsBytesSafe()
			if err != nil {
				return false, err
			}
			mBuffer := codec.NewBuffer(packedArr)
			var metricName string
			err = molecule.MessageEach(mBuffer, func(fieldNum int32, value molecule.Value) (iCont bool, iErr error) {
				if fieldNum == metricSeriesMetricName {
					metricName, iErr = value.AsStringSafe()
					return false, iErr
				}
				return true, nil
			})
			if err != nil {
				return false, err
			}
			if !strings.HasPrefix(metricName, h.cfg.MetricsPrefixFilter) {
				err = ps.Bytes(int(fieldNum), value.Bytes)
			} else {
				dropCount++
			}
		default:
			err = ps.Bytes(int(fieldNum), value.Bytes)
		}

		return err == nil, err
	})
	if err != nil {
		klog.ErrorS(err, "Could not parse protobuf message")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "%v", err)
		return
	}
	h.logDropCount(dropCount, r)

	buf, rw := getWriterForRequest(r)
	_, err = io.Copy(rw, output)
	_ = rw.Close()

	if err != nil {
		klog.ErrorS(err, "Could not encode protobuf")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "%v", err)
		return
	}
	h.proxyRequest(w, r, io.NopCloser(buf))
}

func (h *Handler) MetricsFilter(w http.ResponseWriter, r *http.Request) {
	if h.cfg.MetricsPrefixFilter == "" {
		h.proxyRequest(w, r, r.Body)
		return
	}

	err, rc := getReaderFromRequest(r)
	if err != nil {
		logCouldNotReadBodyError(w, err)
		return
	}

	var payload datadog.MetricsPayload
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
	dropCount := int64(len(payload.Series) - len(filteredSeries))
	h.logDropCount(dropCount, r)

	payload.SetSeries(filteredSeries)

	buf, rw := getWriterForRequest(r)
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

func (h *Handler) logDropCount(dropCount int64, r *http.Request) {
	klog.InfoS("Parsed Metric",
		"drop_count", dropCount,
		"compression", r.Header.Get("Content-Encoding"))
	_ = h.statsDClient.Count(metricsFilteredCountName, dropCount, h.cfg.Tags, 1)
}

func logCouldNotReadBodyError(w http.ResponseWriter, err error) {
	klog.ErrorS(err, "Could not read body")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = fmt.Fprintf(w, "%v", err)
}

func logCouldNotBufferBodyError(w http.ResponseWriter, err error) {
	klog.ErrorS(err, "Could not buffer body")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = fmt.Fprintf(w, "%v", err)
}

func getReaderFromRequest(r *http.Request) (error, io.ReadCloser) {
	var err error
	var rc io.ReadCloser
	switch r.Header.Get("Content-Encoding") {
	case encodingGzip:
		rc, err = gzip.NewReader(r.Body)
	case encodingDeflate:
		rc, err = zlib.NewReader(r.Body)
	default:
		rc = r.Body
	}
	return err, rc
}

func getWriterForRequest(r *http.Request) (*bytes.Buffer, io.WriteCloser) {
	buf := new(bytes.Buffer)
	var rw io.WriteCloser
	switch r.Header.Get("Content-Encoding") {
	case encodingGzip:
		rw = gzip.NewWriter(buf)
	case encodingDeflate:
		rw = zlib.NewWriter(buf)
	default:
		rw = &nopWriterCloser{buf}
	}
	return buf, rw
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
