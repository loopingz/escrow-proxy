package metrics

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNilSafeNoOps(t *testing.T) {
	var m *Metrics

	// None of these should panic.
	m.RecordRequest("serve", "GET", "200", CacheHit, time.Second)
	m.RecordCacheHit(123)
	m.RecordCacheMiss()
	m.RecordUpstreamError(ErrKindTimeout)
	m.SetReindexInProgress(true)
	m.RegisterCAExpiry(nil)
	m.RegisterIndexEntries(nil, nil)

	h := m.Handler()
	if h == nil {
		t.Fatal("Handler() returned nil")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for nil-Metrics handler, got %d", rec.Code)
	}

	shutdown, err := m.StartServer(":0", nil)
	if err != nil {
		t.Fatalf("StartServer on nil Metrics: %v", err)
	}
	_ = shutdown(context.Background())
}

func TestRecordRequest(t *testing.T) {
	m := New()
	m.RecordRequest("serve", "GET", "200", CacheHit, 250*time.Millisecond)
	m.RecordRequest("serve", "GET", "200", CacheHit, 250*time.Millisecond)
	m.RecordRequest("serve", "GET", "502", CacheMiss, 1*time.Second)

	body := scrape(t, m)

	mustContain(t, body, `escrow_proxy_requests_total{cache="hit",method="GET",mode="serve",status="200"} 2`)
	mustContain(t, body, `escrow_proxy_requests_total{cache="miss",method="GET",mode="serve",status="502"} 1`)
	// Duration histogram exposes a _count metric with the label set.
	mustContain(t, body, `escrow_proxy_request_duration_seconds_count{method="GET",mode="serve"} 3`)
}

func TestRecordCache(t *testing.T) {
	m := New()
	m.RecordCacheHit(512)
	m.RecordCacheHit(1024)
	m.RecordCacheMiss()

	body := scrape(t, m)
	mustContain(t, body, "escrow_proxy_cache_hits_total 2")
	mustContain(t, body, "escrow_proxy_cache_misses_total 1")
	mustContain(t, body, "escrow_proxy_cache_bytes_served_total 1536")
}

func TestRecordUpstreamError(t *testing.T) {
	m := New()
	m.RecordUpstreamError(ErrKindTimeout)
	m.RecordUpstreamError(ErrKindTimeout)
	m.RecordUpstreamError(ErrKindTLS)
	m.RecordUpstreamError("") // ignored

	body := scrape(t, m)
	mustContain(t, body, `escrow_proxy_upstream_errors_total{kind="timeout"} 2`)
	mustContain(t, body, `escrow_proxy_upstream_errors_total{kind="tls"} 1`)
}

func TestRegisterCAExpiry(t *testing.T) {
	m := New()
	cert := newTestCert(t, 30*24*time.Hour)
	m.RegisterCAExpiry(cert)

	body := scrape(t, m)
	mustContain(t, body, "escrow_proxy_ca_expiry_seconds ")

	// Sanity check: extract the numeric value and confirm it's positive
	// and below the duration we set (some time has passed during the test).
	val := extractMetricValue(t, body, "escrow_proxy_ca_expiry_seconds")
	if val <= 0 {
		t.Errorf("expected positive expiry, got %v", val)
	}
	if val > float64((30 * 24 * time.Hour).Seconds()) {
		t.Errorf("expiry %v larger than 30d", val)
	}
}

func TestRegisterIndexEntries(t *testing.T) {
	m := New()
	counter := func(ctx context.Context) (int64, error) { return 42, nil }
	m.RegisterIndexEntries(counter, nil)
	m.SetReindexInProgress(true)

	body := scrape(t, m)
	mustContain(t, body, "escrow_proxy_index_entries 42")
	mustContain(t, body, "escrow_proxy_reindex_in_progress 1")

	m.SetReindexInProgress(false)
	body = scrape(t, m)
	mustContain(t, body, "escrow_proxy_reindex_in_progress 0")
}

func TestStartServerAndScrape(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // release so StartServer can bind

	m := New()
	m.RecordCacheHit(7)

	shutdown, err := m.StartServer(addr, nil)
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	defer shutdown(context.Background())

	// Poll briefly for the listener to come up.
	var resp *http.Response
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("healthz never reachable: %v", err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(bodyBytes) != "ok" {
		t.Errorf("healthz: status=%d body=%q", resp.StatusCode, string(bodyBytes))
	}

	resp, err = http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "escrow_proxy_cache_hits_total 1") {
		t.Errorf("missing expected metric in /metrics body:\n%s", string(body))
	}
}

func TestStartServerEmptyAddr(t *testing.T) {
	m := New()
	shutdown, err := m.StartServer("", nil)
	if err != nil {
		t.Fatalf("StartServer with empty addr: %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown is nil")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

func TestStartServerBindFailure(t *testing.T) {
	// Bind a port, then try to start metrics on the same one.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	m := New()
	_, err = m.StartServer(ln.Addr().String(), nil)
	if err == nil {
		t.Fatal("expected bind failure, got nil")
	}
}

func TestClassifyUpstreamError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"deadline", context.DeadlineExceeded, ErrKindTimeout},
		{"net timeout", &timeoutErr{}, ErrKindTimeout},
		{"dial op error", &net.OpError{Op: "dial", Err: errors.New("nope")}, ErrKindDial},
		{"x509 unknown auth", x509.UnknownAuthorityError{}, ErrKindTLS},
		{"x509 hostname", x509.HostnameError{Host: "example.com"}, ErrKindTLS},
		{"generic", errors.New("some other error"), ErrKindOther},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyUpstreamError(tc.err)
			if got != tc.want {
				t.Errorf("ClassifyUpstreamError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// --- helpers ---

type timeoutErr struct{}

func (*timeoutErr) Error() string   { return "timeout" }
func (*timeoutErr) Timeout() bool   { return true }
func (*timeoutErr) Temporary() bool { return true }

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape: status %d, body: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func mustContain(t *testing.T, body, sub string) {
	t.Helper()
	if !strings.Contains(body, sub) {
		t.Errorf("scrape body missing %q\n--- full body ---\n%s", sub, body)
	}
}

// extractMetricValue finds the first non-help, non-type line starting
// with name+" " and parses the trailing number.
func extractMetricValue(t *testing.T, body, name string) float64 {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, name) {
			continue
		}
		// line is like: "name 1234.56" or "name{labels} 1234.56"
		// Strip name + optional {labels}; the remainder is " value".
		rest := strings.TrimPrefix(line, name)
		if strings.HasPrefix(rest, "{") {
			end := strings.Index(rest, "}")
			if end < 0 {
				continue
			}
			rest = rest[end+1:]
		}
		rest = strings.TrimSpace(rest)
		var v float64
		if _, err := fmt.Sscanf(rest, "%f", &v); err == nil {
			return v
		}
	}
	t.Fatalf("metric %q not found in body:\n%s", name, body)
	return 0
}

func newTestCert(t *testing.T, validFor time.Duration) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(validFor),
		IsCA:         true,
	}
	raw, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
