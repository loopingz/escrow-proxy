package proxy

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/elazarl/goproxy"
	"github.com/loopingz/escrow-proxy/internal/metrics"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: connection refused")
}

// Upstream errors must be converted into a synthesized 502 (with the marker
// header) rather than returned: goproxy's MITM loop skips filterResponse on a
// RoundTrip error, so returning an error would prevent HandleResponse from
// ever seeing the failure — and from serving a stale revalidation fallback.
func TestRedirectFollower_UpstreamError_Synthesizes502(t *testing.T) {
	rf := newRedirectFollower(failingTransport{}, metrics.New())

	req, _ := http.NewRequest("GET", "https://example.com/simple/urllib3/", nil)
	resp, err := rf.RoundTrip(req, nil)
	if err != nil {
		t.Fatalf("expected synthesized response, got error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected synthesized response, got nil")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502", resp.StatusCode)
	}
	if resp.Header.Get(upstreamErrorHeader) == "" {
		t.Fatal("synthesized response missing upstream-error marker header")
	}
}

// The marker header is internal: HandleResponse must strip it when passing
// the synthesized 502 through, and must serve a stale revalidation fallback
// for it like any other non-2xx upstream result.
func TestHandleResponse_UpstreamErrorMarker_StrippedAndFallbackServed(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	h, c := newRevalidateHandler(t, func() time.Time { return now })

	// Stale fallback armed: synthesized 502 serves the cached body.
	reqURL := "https://pypi.example/simple/urllib3/"
	seedCache(t, c, h, reqURL, "OLD-INDEX-BODY", now.Add(-10*time.Minute))
	req := mkReq(t, "GET", reqURL)
	ctx := &goproxy.ProxyCtx{Req: req}
	if _, resp := h.HandleRequest(req, ctx); resp != nil {
		t.Fatal("stale entry should defer to upstream")
	}
	synth := goproxy.NewResponse(req, goproxy.ContentTypeText, http.StatusBadGateway, "boom")
	synth.Header.Set(upstreamErrorHeader, "1")
	got := h.HandleResponse(synth, ctx)
	if got.StatusCode != 200 || string(readAll(t, got.Body)) != "OLD-INDEX-BODY" {
		t.Fatalf("expected stale fallback served, got status %d", got.StatusCode)
	}

	// No fallback: the 502 passes through without the internal marker.
	missURL := "https://pypi.example/simple/requests/"
	missReq := mkReq(t, "GET", missURL)
	missCtx := &goproxy.ProxyCtx{Req: missReq}
	if _, resp := h.HandleRequest(missReq, missCtx); resp != nil {
		t.Fatal("cache miss should defer to upstream")
	}
	synth = goproxy.NewResponse(missReq, goproxy.ContentTypeText, http.StatusBadGateway, "boom")
	synth.Header.Set(upstreamErrorHeader, "1")
	got = h.HandleResponse(synth, missCtx)
	if got.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 passthrough, got %d", got.StatusCode)
	}
	if got.Header.Get(upstreamErrorHeader) != "" {
		t.Fatal("internal marker header leaked to client")
	}
}
