package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"
)

type CertCache struct {
	ca      *CA
	mu      sync.Mutex
	cache   map[string]*cacheEntry
	order   []string
	maxSize int
}

type cacheEntry struct {
	cert *tls.Certificate
}

func NewCertCache(ca *CA, maxSize int) *CertCache {
	return &CertCache{
		ca:      ca,
		cache:   make(map[string]*cacheEntry),
		maxSize: maxSize,
	}
}

func (c *CertCache) GetOrCreate(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.cache[host]; ok {
		return entry.cert, nil
	}

	cert, err := c.generate(host)
	if err != nil {
		return nil, err
	}

	c.cache[host] = &cacheEntry{cert: cert}
	c.order = append(c.order, host)

	if len(c.order) > c.maxSize {
		evict := c.order[0]
		c.order = c.order[1:]
		delete(c.cache, evict)
	}

	return cert, nil
}

func (c *CertCache) generate(host string) (*tls.Certificate, error) {
	// Strip port if present (CONNECT requests include it, e.g. "github.com:443")
	hostname, _, err := net.SplitHostPort(host)
	if err != nil {
		// No port present, use as-is
		hostname = host
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating leaf key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generating serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hostname},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{hostname},
	}

	raw, err := x509.CreateCertificate(rand.Reader, template, c.ca.Cert, &key.PublicKey, c.ca.Key)
	if err != nil {
		return nil, fmt.Errorf("creating leaf cert: %w", err)
	}

	leaf, err := x509.ParseCertificate(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing leaf cert: %w", err)
	}

	return &tls.Certificate{
		Certificate: [][]byte{raw, c.ca.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}
