package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/elazarl/goproxy"
)

// TestHandleRequest_NilURL exercises the case where goproxy's MITM path
// invokes our request handler with req.URL == nil. This happens when
// url.Parse fails inside goproxy.handleHttps (https.go:273); goproxy still
// calls filterRequest before checking the parse error, so our handler must
// not dereference req.URL.
func TestHandleRequest_NilURL(t *testing.T) {
	h := &Handler{
		methods: map[string]bool{},
		mode:    ModeServe,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	req := &http.Request{
		Method: "GET",
		Host:   "example.com",
		Header: http.Header{},
		URL:    nil,
	}
	ctx := &goproxy.ProxyCtx{}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("HandleRequest panicked on nil URL: %v", r)
		}
	}()

	gotReq, gotResp := h.HandleRequest(req, ctx)
	if gotReq != req {
		t.Fatalf("returned request: got %p, want %p", gotReq, req)
	}
	if gotResp != nil {
		t.Fatalf("returned response: got %v, want nil (let goproxy hit its err path)", gotResp)
	}
}
