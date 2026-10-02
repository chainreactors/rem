//go:build !tinygo

package xtls

import (
	"crypto/tls"
	"crypto/x509"

	ucert "github.com/chainreactors/utils/cert"
)

// CertOption is an alias for the shared cert package template option.
type CertOption = ucert.TemplateOption

// Re-export option constructors for backward compatibility.
var (
	WithCommonName = ucert.WithSubjectCN
	WithSubject    = ucert.WithFullSubject
	WithValidity   = ucert.WithRandomValidity
)

func newCustomTLSKeyPair(certfile, keyfile string) (*tls.Certificate, error) {
	tlsCert, err := tls.LoadX509KeyPair(certfile, keyfile)
	if err != nil {
		return nil, err
	}
	return &tlsCert, nil
}

func NewRandomTLSKeyPair(opts ...CertOption) *tls.Certificate {
	certPEM, keyPEM, err := ucert.GenerateSelfSignedCert(0, opts...)
	if err != nil {
		panic(err)
	}
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(err)
	}
	return &tlsCert
}

func newCertPool(caPath string) (*x509.CertPool, error) {
	return ucert.LoadCertPoolFromFile(caPath)
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
