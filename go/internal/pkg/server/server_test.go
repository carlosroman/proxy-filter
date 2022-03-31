package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/carlosroman/proxy-filter/go/internal/pkg/server"
)

func TestHandler_ProxyHandle(t *testing.T) {

	type result struct {
		path,
		body,
		contentRequestTypeHeader,
		apiKey,
		userAgent,
		method string
		params url.Values
	}

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
				if tc.expectedResponse != "" {
					_, _ = io.WriteString(w, tc.expectedResponse)
				}
			}))
			defer ts.Close()

			cfg := server.Config{
				BaseEndpoint: ts.URL,
			}

			h := server.NewHandler(cfg, ts.Client())

			ps := httptest.NewServer(http.HandlerFunc(h.ProxyHandle))
			defer ps.Close()

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

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.NotNil(t, resp)
			assert.Equal(t, 418, resp.StatusCode)

			actual := <-resultChan
			assert.NotNil(t, actual)
			assert.Equal(t, expectedPath, actual.path)
			assert.Equal(t, tc.method, actual.method)
			assert.Equal(t, "text/test", actual.contentRequestTypeHeader)
			assert.Equal(t, "SECRET API KEY", actual.apiKey)
			assert.Equal(t, "unit-test", actual.userAgent)
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
