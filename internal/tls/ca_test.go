package tls_test

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	tlspkg "github.com/loopingz/escrow-proxy/internal/tls"
)

func TestGenerateCA(t *testing.T) {
	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	if ca.Cert == nil {
		t.Fatal("expected non-nil cert")
	}
	if ca.Key == nil {
		t.Fatal("expected non-nil key")
	}
	if !ca.Cert.IsCA {
		t.Fatal("expected CA certificate")
	}
}

func TestSaveAndLoadCA(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	ca, err := tlspkg.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	if err := tlspkg.SaveCA(ca, certPath, keyPath); err != nil {
		t.Fatalf("SaveCA: %v", err)
	}

	loaded, err := tlspkg.LoadCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}

	if !loaded.Cert.Equal(ca.Cert) {
		t.Fatal("loaded cert doesn't match original")
	}
}

func TestEnsureCA_GeneratesOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	ca, err := tlspkg.EnsureCA(dir, "", "")
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	if ca.Cert == nil {
		t.Fatal("expected non-nil cert")
	}

	if _, err := os.Stat(filepath.Join(dir, "ca.crt")); err != nil {
		t.Fatalf("ca.crt not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.key")); err != nil {
		t.Fatalf("ca.key not found: %v", err)
	}
}

func TestEnsureCA_ReusesExisting(t *testing.T) {
	dir := t.TempDir()
	ca1, _ := tlspkg.EnsureCA(dir, "", "")
	ca2, _ := tlspkg.EnsureCA(dir, "", "")

	if !ca1.Cert.Equal(ca2.Cert) {
		t.Fatal("expected same CA on second call")
	}
}

func TestExportCAPEM(t *testing.T) {
	ca, _ := tlspkg.GenerateCA()
	pem := tlspkg.ExportCAPEM(ca)
	if len(pem) == 0 {
		t.Fatal("expected non-empty PEM")
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("PEM not parseable as certificate")
	}
}
