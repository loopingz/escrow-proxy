package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/proxy"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

// Drives a HEAD request through a real goproxy.ProxyHttpServer (the same
// server type proxy.New wires up in production), not just Handler.HandleResponse
// in isolation. That distinction matters: goproxy's own response writer
// (http.go's handleHttp) deletes Content-Length whenever it sees resp.Body
// change identity across HandleResponse, independent of anything the header
// says. A unit test that calls HandleResponse directly and inspects the
// returned *http.Response never exercises that writer, so it cannot see this
// class of bug -- no unit test can: the origin already sends a valid
// Content-Length here, so the only way to lose it is goproxy's own writer
// clobbering it because HandleResponse rebuilt resp.Body on this cache miss.
func TestHEAD_ContentLengthSurvivesRealProxyRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1788")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	client := setupDigestProxy(t, proxy.DigestMismatchError, c)

	req, _ := http.NewRequest(http.MethodHead, upstream.URL+"/v2/library/foo/manifests/latest", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()

	if resp.ContentLength != 1788 {
		t.Errorf("ContentLength = %d, want 1788", resp.ContentLength)
	}
	if got := resp.Header.Get("Content-Length"); got != "1788" {
		t.Errorf("Content-Length header = %q, want %q", got, "1788")
	}
}
