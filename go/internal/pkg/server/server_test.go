package server_test

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/DataDog/datadog-api-client-go/api/v1/datadog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/carlosroman/proxy-filter/go/internal/pkg/server"
)

type result struct {
	path,
	body,
	contentRequestTypeHeader,
	apiKey,
	userAgent,
	method string
	params url.Values
}

func TestHandler_ProxyHandle(t *testing.T) {

	tests := []struct {
		name             string
		method           string
		sentBody         string
		expectedResponse string
		expectedParams   []struct{ key, value string }
	}{
		{
			name:             "GET",
			method:           "GET",
			expectedResponse: "{expected response}",
		},
		{
			name:     "POST",
			method:   "POST",
			sentBody: "{expected payload}",
		},
		{
			name:             "GET with params",
			method:           "GET",
			expectedResponse: "{expected response}",
			expectedParams: []struct{ key, value string }{{
				key:   "key_one",
				value: "value_one",
			}, {
				key:   "key_two",
				value: "value_two",
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given server is running
			resultChan, ts, h, _ := setupCaptureServer(t, tc.expectedResponse, "")
			ps := httptest.NewServer(http.HandlerFunc(h.ProxyHandle))

			defer func() {
				ts.Close()
				ps.Close()
			}()

			// And we create a request
			expectedPath := "/some/test/path"
			req, err := http.NewRequest(tc.method, ps.URL+expectedPath, nil)
			if tc.sentBody != "" {
				req, err = http.NewRequest(tc.method, ps.URL+expectedPath, strings.NewReader(tc.sentBody))
			}
			if len(tc.expectedParams) > 0 {
				q := req.URL.Query()
				for _, item := range tc.expectedParams {
					q.Add(item.key, item.value)
				}
				req.URL.RawQuery = q.Encode()
			}
			require.NoError(t, err)
			req.Header.Add("Content-Type", "text/test")
			req.Header.Add("User-Agent", "unit-test")
			req.Header.Add("DD-API-KEY", "SECRET API KEY")

			// When we make the request
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.NotNil(t, resp)
			assert.Equal(t, 418, resp.StatusCode)

			// Then a request is proxied to test server
			actual := <-resultChan
			assert.NotNil(t, actual)
			assert.Equal(t, expectedPath, actual.path)
			assert.Equal(t, tc.method, actual.method)
			assert.Equal(t, "text/test", actual.contentRequestTypeHeader)
			assert.Equal(t, "SECRET API KEY", actual.apiKey)
			assert.Equal(t, "unit-test", actual.userAgent)

			// And the payload matches
			rb, err := io.ReadAll(resp.Body)
			assert.NoError(t, err)
			assert.Equal(t, tc.expectedResponse, string(rb))
			if len(tc.expectedParams) > 0 {
				expectedParams := make(url.Values)
				for _, item := range tc.expectedParams {
					expectedParams.Add(item.key, item.value)
				}
				assert.Equal(t, expectedParams.Encode(), actual.params.Encode())
			}
		})
	}
}

func TestHandler_MetricsFilter(t *testing.T) {
	type Compress int64
	const (
		None Compress = iota
		Gzip
		Deflate
	)

	tests := []struct {
		name            string
		filterPrefix    string
		payload         datadog.MetricsPayload
		expectedPayload datadog.MetricsPayload
		expectedCount   int64
		compressRequest Compress
	}{
		{
			name:            "Filter nothing",
			payload:         defaultMetricsPayload([]string{"metric.one", "metric.two"}),
			expectedPayload: defaultMetricsPayload([]string{"metric.one", "metric.two"}),
		},
		{
			name:            "Filter no matches",
			filterPrefix:    "some.metric",
			payload:         defaultMetricsPayload([]string{"metric.one", "metric.two"}),
			expectedPayload: defaultMetricsPayload([]string{"metric.one", "metric.two"}),
		},
		{
			name:            "Filter metrics",
			filterPrefix:    "some.metric",
			payload:         defaultMetricsPayload([]string{"metric.one", "some.metric.load", "metric.two"}),
			expectedPayload: defaultMetricsPayload([]string{"metric.one", "metric.two"}),
		},
		{
			name:            "Filter nothing gzip",
			payload:         defaultMetricsPayload([]string{"metric.gzip.one", "metric.two"}),
			expectedPayload: defaultMetricsPayload([]string{"metric.gzip.one", "metric.two"}),
			compressRequest: Gzip,
		},
		{
			name:            "Filter no matches gzip",
			filterPrefix:    "some.metric",
			payload:         defaultMetricsPayload([]string{"metric.gzip.one", "metric.two"}),
			expectedPayload: defaultMetricsPayload([]string{"metric.gzip.one", "metric.two"}),
			compressRequest: Gzip,
		},
		{
			name:            "Filter metrics gzip",
			filterPrefix:    "some.metric",
			payload:         defaultMetricsPayload([]string{"metric.gzip.one", "some.metric.load", "metric.two"}),
			expectedPayload: defaultMetricsPayload([]string{"metric.gzip.one", "metric.two"}),
			compressRequest: Gzip,
		},
		{
			name:            "Filter nothing deflate",
			payload:         defaultMetricsPayload([]string{"metric.deflate.one", "metric.two"}),
			expectedPayload: defaultMetricsPayload([]string{"metric.deflate.one", "metric.two"}),
			compressRequest: Deflate,
		},
		{
			name:            "Filter no matches deflate",
			filterPrefix:    "some.metric",
			payload:         defaultMetricsPayload([]string{"metric.deflate.one", "metric.two"}),
			expectedPayload: defaultMetricsPayload([]string{"metric.deflate.one", "metric.two"}),
			compressRequest: Deflate,
		},
		{
			name:            "Filter metrics deflate",
			filterPrefix:    "some.metric",
			payload:         defaultMetricsPayload([]string{"metric.deflate.one", "some.metric.load", "metric.two"}),
			expectedPayload: defaultMetricsPayload([]string{"metric.deflate.one", "metric.two"}),
			compressRequest: Deflate,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given server is running
			resultChan, ts, h, sc := setupCaptureServer(t, "/filter/endpoint", tc.filterPrefix)
			ps := httptest.NewServer(http.HandlerFunc(h.MetricsFilter))

			defer func() {
				ts.Close()
				ps.Close()
			}()

			// And we create a request
			b := new(bytes.Buffer)
			var err error

			switch tc.compressRequest {
			case Gzip:
				gz := gzip.NewWriter(b)
				err = json.NewEncoder(gz).Encode(tc.payload)
				_ = gz.Close()
			case Deflate:
				gz := zlib.NewWriter(b)
				err = json.NewEncoder(gz).Encode(tc.payload)
				_ = gz.Close()
			default:
				err = json.NewEncoder(b).Encode(tc.payload)
			}
			require.NoError(t, err)

			req, err := http.NewRequest("POST", ps.URL+"/filter/endpoint", b)
			require.NoError(t, err)
			req.Header.Add("Content-Type", "application/json")

			switch tc.compressRequest {
			case Gzip:
				req.Header.Add("Content-Encoding", "gzip")
			case Deflate:
				req.Header.Add("Content-Encoding", "deflate")
			}

			// When we make the request
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			respBody, err := ioutil.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, 418, resp.StatusCode, fmt.Sprintf("Got an error: %v", string(respBody)))

			// Then a request is proxied to test server
			actual := <-resultChan
			assert.NotNil(t, actual)
			assert.Equal(t, "POST", actual.method)
			assert.Equal(t, "application/json", actual.contentRequestTypeHeader)
			assert.Equal(t, "/filter/endpoint", actual.path)

			// And the payload matches
			var actualPayload datadog.MetricsPayload
			switch tc.compressRequest {
			case Gzip:
				gz, err := gzip.NewReader(strings.NewReader(actual.body))
				require.NoError(t, err)
				err = json.NewDecoder(gz).Decode(&actualPayload)
			case Deflate:
				gz, err := zlib.NewReader(strings.NewReader(actual.body))
				require.NoError(t, err)
				err = json.NewDecoder(gz).Decode(&actualPayload)
			default:
				err = json.Unmarshal([]byte(actual.body), &actualPayload)
			}

			require.NoError(t, err)

			assert.Equal(t, actualPayload, tc.expectedPayload)
			value := len(tc.payload.Series) - len(tc.expectedPayload.Series)
			// called true only if we are going to filter metrics
			called := tc.filterPrefix != ""
			sc.assertCount(t, "proxy_filter.filtered_metrics.count", int64(value), []string{"one", "two", "three"}, 1, called)
		})
	}
}

func setupCaptureServer(t *testing.T, expectedResponse, metricsPrefixFilter string) (chan result, *httptest.Server, server.Handler, *stubStatsdClient) {
	resultChan := make(chan result, 1)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		res := result{
			path:                     r.URL.Path,
			body:                     string(body),
			contentRequestTypeHeader: r.Header.Get("Content-Type"),
			method:                   r.Method,
			apiKey:                   r.Header.Get("DD-API-KEY"),
			userAgent:                r.Header.Get("User-Agent"),
			params:                   r.URL.Query(),
		}
		defer func(res result) {
			resultChan <- res
		}(res)
		w.Header().Add("Content-Type", "application/test")
		w.WriteHeader(418)
		if expectedResponse != "" {
			_, _ = io.WriteString(w, expectedResponse)
		}
	}))

	cfg := server.Config{
		BaseEndpoint:        ts.URL,
		MetricsPrefixFilter: metricsPrefixFilter,
		Tags:                []string{"one", "two", "three"},
	}

	sd := &stubStatsdClient{}
	h := server.NewHandler(cfg, ts.Client(), sd)

	return resultChan, ts, h, sd
}

type stubStatsdClient struct {
	name   string
	value  int64
	tags   []string
	rate   float64
	called bool
	sync.Mutex
}

func (s *stubStatsdClient) Count(name string, value int64, tags []string, rate float64) (err error) {
	s.Lock()
	defer s.Unlock()
	s.name = name
	s.value = value
	s.tags = tags
	s.rate = rate
	s.called = true
	return
}

func (s *stubStatsdClient) assertCount(t *testing.T, name string, value int64, tags []string, rate float64, called bool) {
	s.Lock()
	defer s.Unlock()
	if !called {
		require.False(t, called)
		return
	}
	require.True(t, s.called)
	assert.Equal(t, name, s.name)
	assert.Equal(t, tags, s.tags)
	assert.Equal(t, rate, s.rate)
	assert.Equal(t, value, s.value)
}

func defaultMetricsPayload(metricName []string) (payload datadog.MetricsPayload) {
	payload.Series = make([]datadog.Series, len(metricName))
	for i := range metricName {
		payload.Series[i] = datadog.Series{
			Metric: metricName[i],
			Type:   datadog.PtrString("gauge"),
			Points: [][]*float64{
				{
					datadog.PtrFloat64(float64(1231231231232)),
					datadog.PtrFloat64(float64(1)),
				},
			},
			Tags: &[]string{
				"test:ExampleSubmitmetricsreturnsPayloadacceptedresponse",
			},
		}
	}
	return
}
