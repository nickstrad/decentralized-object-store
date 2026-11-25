// Package httpapitest provides test support for clients of the object-store
// HTTP API.
package httpapitest

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
)

// InProcess returns an http.RoundTripper that serves each request with h
// without sockets: the request and response still cross a real HTTP/1.1
// encoding (written to bytes and parsed back), so the request-target, headers
// and bodies are exactly what a server on the network would see.
func InProcess(h http.Handler) http.RoundTripper { return inProcess{h} }

type inProcess struct{ h http.Handler }

func (p inProcess) RoundTrip(req *http.Request) (*http.Response, error) {
	var buf bytes.Buffer
	if err := req.Write(&buf); err != nil {
		return nil, fmt.Errorf("httpapitest: write request: %w", err)
	}
	sreq, err := http.ReadRequest(bufio.NewReader(&buf))
	if err != nil {
		return nil, fmt.Errorf("httpapitest: read request: %w", err)
	}
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, sreq.WithContext(req.Context()))
	var out bytes.Buffer
	if err := rec.Result().Write(&out); err != nil {
		return nil, fmt.Errorf("httpapitest: write response: %w", err)
	}
	return http.ReadResponse(bufio.NewReader(&out), req)
}
