package server

import (
	"fmt"
	"io"
	"net/http"
)

type Config struct {
	BaseEndpoint string
}

func NewHandler(cfg Config, httpClient *http.Client) Handler {
	return Handler{cfg: cfg, httpClient: httpClient}
}

type Handler struct {
	cfg        Config
	httpClient *http.Client
}

func (h *Handler) ProxyHandle(w http.ResponseWriter, r *http.Request) {
	url := h.cfg.BaseEndpoint + r.URL.Path
	req, err := http.NewRequest(r.Method, url, r.Body)
	req.URL.RawQuery = r.URL.RawQuery
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, "%v", err)
		return
	}

	for key := range r.Header {
		req.Header.Add(key, r.Header.Get(key))
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Println(err)
		return
	}

	defer resp.Body.Close()
	for key := range resp.Header {
		w.Header().Add(key, resp.Header.Get(key))
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	//fmt.Println(fmt.Sprintf("Sent request to %s with Content-Encoding %s, got %d", url, r.Header.Get("Content-Encoding"), resp.StatusCode))
}
