package tls_test

import (
	"testing"

	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
)

func TestCertCache_GeneratesLeafCert(t *testing.T) {
	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	cache := tlspkg.NewCertCache(ca, 100)
	tlsCert, err := cache.GetOrCreate("example.com")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if tlsCert.Leaf == nil {
		t.Fatal("expected non-nil leaf")
	}
	if tlsCert.Leaf.Subject.CommonName != "example.com" {
		t.Fatalf("expected CN=example.com, got %s", tlsCert.Leaf.Subject.CommonName)
	}
}

func TestCertCache_CachesResult(t *testing.T) {
	ca, _ := tlspkg.GenerateCA()
	cache := tlspkg.NewCertCache(ca, 100)

	cert1, _ := cache.GetOrCreate("example.com")
	cert2, _ := cache.GetOrCreate("example.com")

	if cert1.Leaf.SerialNumber.Cmp(cert2.Leaf.SerialNumber) != 0 {
		t.Fatal("expected same cert on second call (cached)")
	}
}

func TestCertCache_DifferentHostsDifferentCerts(t *testing.T) {
	ca, _ := tlspkg.GenerateCA()
	cache := tlspkg.NewCertCache(ca, 100)

	cert1, _ := cache.GetOrCreate("example.com")
	cert2, _ := cache.GetOrCreate("other.com")

	if cert1.Leaf.SerialNumber.Cmp(cert2.Leaf.SerialNumber) == 0 {
		t.Fatal("expected different certs for different hosts")
	}
}
