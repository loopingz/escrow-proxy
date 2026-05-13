package tls

import (
	"testing"
	"time"
)

func TestCertCache_RegeneratesExpiredLeaf(t *testing.T) {
	ca, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	cache := NewCertCache(ca, 100)

	cert1, err := cache.GetOrCreate("example.com")
	if err != nil {
		t.Fatalf("first GetOrCreate: %v", err)
	}

	// Advance the clock past the leaf cert's NotAfter (leaves are valid 24h).
	cache.now = func() time.Time { return time.Now().Add(48 * time.Hour) }

	cert2, err := cache.GetOrCreate("example.com")
	if err != nil {
		t.Fatalf("second GetOrCreate: %v", err)
	}

	if cert1.Leaf.SerialNumber.Cmp(cert2.Leaf.SerialNumber) == 0 {
		t.Fatal("expected a new cert after expiry, got the stale cached one")
	}
}

func TestCertCache_RegeneratesNearExpiryLeaf(t *testing.T) {
	// Certs about to expire mid-connection should also be regenerated.
	ca, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	cache := NewCertCache(ca, 100)

	cert1, err := cache.GetOrCreate("example.com")
	if err != nil {
		t.Fatalf("first GetOrCreate: %v", err)
	}

	// Move clock to 30 minutes before NotAfter — within the safety margin.
	cache.now = func() time.Time { return cert1.Leaf.NotAfter.Add(-30 * time.Minute) }

	cert2, err := cache.GetOrCreate("example.com")
	if err != nil {
		t.Fatalf("second GetOrCreate: %v", err)
	}

	if cert1.Leaf.SerialNumber.Cmp(cert2.Leaf.SerialNumber) == 0 {
		t.Fatal("expected a new cert when within safety margin of expiry")
	}
}
