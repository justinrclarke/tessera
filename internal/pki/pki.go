package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

type Material struct {
	CertPEM   []byte
	KeyPEM    []byte
	NotBefore time.Time
	NotAfter  time.Time
}

func NewCA(now time.Time) (Material, error) {
	return issue(now, 10*365*24*time.Hour, nil, nil, "tessera-ca", true)
}

func Issue(ca Material, nodeID string, now time.Time, ttl time.Duration) (Material, error) {
	caCert, caKey, err := parse(ca)
	if err != nil {
		return Material{}, err
	}
	return issue(now, ttl, caCert, caKey, nodeID, false)
}

func NeedsRenewal(notBefore, notAfter, now time.Time) bool {
	if notAfter.IsZero() {
		return false
	}
	if !now.Before(notAfter) {
		return true
	}
	life := notAfter.Sub(notBefore)
	if life <= 0 {
		return false
	}
	return notAfter.Sub(now) < life/2
}

func issue(now time.Time, ttl time.Duration, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, cn string, isCA bool) (Material, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Material{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Material{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	if isCA {
		tmpl.IsCA = true
		tmpl.KeyUsage |= x509.KeyUsageCertSign
		tmpl.BasicConstraintsValid = true
		parent = tmpl
		parentKey = key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		return Material{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return Material{}, err
	}
	return Material{
		CertPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:    pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		NotBefore: tmpl.NotBefore,
		NotAfter:  tmpl.NotAfter,
	}, nil
}

func parse(m Material) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, _ := pem.Decode(m.CertPEM)
	kb, _ := pem.Decode(m.KeyPEM)
	if cb == nil || kb == nil {
		return nil, nil, fmt.Errorf("invalid pem")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}
