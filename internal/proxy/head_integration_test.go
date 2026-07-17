package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/loopingz/escrow-proxy/internal/cache"
	"github.com/loopingz/escrow-proxy/internal/proxy"
	"github.com/loopingz/escrow-proxy/internal/storage"
)

// These drive a HEAD request through a real goproxy.ProxyHttpServer (the same
// server type proxy.New wires up in production), not just Handler.HandleResponse
// in isolation. That distinction matters: goproxy's own response writer
// (http.go's handleHttp) deletes Content-Length whenever it sees resp.Body
// change identity across HandleResponse, independent of anything the header
// says. A unit test that calls HandleResponse directly and inspects the
// returned *http.Response never exercises that writer, so it cannot see this
// class of bug -- these tests exist to close exactly that gap.

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

func TestHEAD_ResolvedContentLengthSurvivesRealProxyRoundTrip(t *testing.T) {
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","x":"padding-to-a-nontrivial-length"}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			// Reproduce the failure that reaches escrow-proxy in practice: a
			// HEAD 200 with no Content-Length at all. Hijack and write raw
			// bytes, since the stdlib server would otherwise infer one.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\n" +
				"Content-Type: application/vnd.oci.image.manifest.v1+json\r\n\r\n"))
			_ = conn.Close()
			return
		}
		_, _ = w.Write(manifest) // GET: stdlib sets Content-Length = len(manifest)
	}))
	defer upstream.Close()

	store := storage.NewLocal(t.TempDir())
	c := cache.New(store)
	client := setupDigestProxy(t, proxy.DigestMismatchError, c)

	req, _ := http.NewRequest(http.MethodHead, upstream.URL+"/v2/library/foo/manifests/sha256:deadbeef", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()

	want := int64(len(manifest))
	if resp.ContentLength != want {
		t.Errorf("ContentLength = %d, want %d", resp.ContentLength, want)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.FormatInt(want, 10) {
		t.Errorf("Content-Length header = %q, want %d", got, want)
	}
}
