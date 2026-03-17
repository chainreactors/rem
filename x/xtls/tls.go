//go:build !tinygo

package xtls

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	mathrand "math/rand"
	"net"
	"os"
	"time"
)

func newCustomTLSKeyPair(certfile, keyfile string) (*tls.Certificate, error) {
	tlsCert, err := tls.LoadX509KeyPair(certfile, keyfile)
	if err != nil {
		return nil, err
	}
	return &tlsCert, nil
}

func NewRandomTLSKeyPair(opts ...CertOption) *tls.Certificate {
	cfg := &certConfig{}
	for _, o := range opts {
		o(cfg)
	}

	keySize := cfg.keySize
	if keySize == 0 {
		keySize = randomKeySize()
	}

	key, err := rsa.GenerateKey(rand.Reader, keySize)
	if err != nil {
		panic(err)
	}

	validFor := cfg.validFor
	if validFor == 0 {
		validFor = randomValidFor()
	}

	// Random NotBefore offset: 0-30 days in the past
	notBefore := time.Now().Add(-time.Duration(mathrand.Intn(30)) * 24 * time.Hour)
	notAfter := notBefore.Add(validFor)

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(err)
	}

	var subject pkix.Name
	if cfg.subject != nil {
		subject = *cfg.subject
	} else {
		subject = randomSubject(cfg.commonName)
	}

	template := x509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               subject,
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	// Add SAN based on CN
	cn := subject.CommonName
	if ip := net.ParseIP(cn); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else if cn != "" {
		template.DNSNames = []string{cn}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(err)
	}
	return &tlsCert
}

func newCertPool(caPath string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()

	caCrt, err := os.ReadFile(caPath)
	if err != nil {
		return nil, err
	}

	pool.AppendCertsFromPEM(caCrt)

	return pool, nil
}

func NewServerTLSConfig(certPath, keyPath, caPath string, opts ...CertOption) (*tls.Config, error) {
	base := &tls.Config{
		InsecureSkipVerify: true,
	}

	if certPath == "" || keyPath == "" {
		// server will generate tls conf by itself
		cert := NewRandomTLSKeyPair(opts...)
		base.Certificates = []tls.Certificate{*cert}
	} else {
		cert, err := newCustomTLSKeyPair(certPath, keyPath)
		if err != nil {
			return nil, err
		}

		base.Certificates = []tls.Certificate{*cert}
	}

	if caPath != "" {
		pool, err := newCertPool(caPath)
		if err != nil {
			return nil, err
		}

		base.ClientAuth = tls.RequireAndVerifyClientCert
		base.ClientCAs = pool
	}

	return base, nil
}

func NewClientTLSConfig(certPath, keyPath, caPath, serverName string, opts ...CertOption) (*tls.Config, error) {
	base := &tls.Config{}

	if certPath != "" && keyPath != "" {
		cert, err := newCustomTLSKeyPair(certPath, keyPath)
		if err != nil {
			return nil, err
		}

		base.Certificates = []tls.Certificate{*cert}
	}

	base.ServerName = serverName

	if caPath != "" {
		pool, err := newCertPool(caPath)
		if err != nil {
			return nil, err
		}

		base.RootCAs = pool
		base.InsecureSkipVerify = false
	} else {
		base.InsecureSkipVerify = true
	}

	return base, nil
}

