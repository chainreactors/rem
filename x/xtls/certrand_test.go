//go:build !tinygo

package xtls

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	ucert "github.com/chainreactors/utils/cert"
)

func TestNewRandomTLSKeyPairBackwardCompat(t *testing.T) {
	cert := NewRandomTLSKeyPair()
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("nil or empty certificate")
	}
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if x509Cert.Subject.CommonName == "" {
		t.Error("CommonName is empty")
	}
	if len(x509Cert.Subject.Organization) == 0 || x509Cert.Subject.Organization[0] == "" {
		t.Error("Organization is empty")
	}
}

func TestWithCommonName(t *testing.T) {
	cert := NewRandomTLSKeyPair(WithCommonName("test.example.com"))
	x509Cert, _ := x509.ParseCertificate(cert.Certificate[0])
	if x509Cert.Subject.CommonName != "test.example.com" {
		t.Fatalf("expected CN test.example.com, got %s", x509Cert.Subject.CommonName)
	}
	if len(x509Cert.DNSNames) == 0 || x509Cert.DNSNames[0] != "test.example.com" {
		t.Fatalf("expected SAN DNS name, got %v", x509Cert.DNSNames)
	}
}

func TestWithSubject(t *testing.T) {
	subject := pkix.Name{
		CommonName:   "custom.test",
		Organization: []string{"Custom Org"},
		Country:      []string{"DE"},
		Province:     []string{"Bavaria"},
		Locality:     []string{"Munich"},
	}
	cert := NewRandomTLSKeyPair(WithSubject(subject))
	x509Cert, _ := x509.ParseCertificate(cert.Certificate[0])
	if x509Cert.Subject.CommonName != "custom.test" {
		t.Fatalf("expected CN custom.test, got %s", x509Cert.Subject.CommonName)
	}
	if x509Cert.Subject.Organization[0] != "Custom Org" {
		t.Fatalf("expected org Custom Org, got %s", x509Cert.Subject.Organization[0])
	}
}

func TestWithValidityWindow(t *testing.T) {
	start := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	validity := 180 * 24 * time.Hour
	cert := NewRandomTLSKeyPair(ucert.WithValidityWindow(start, validity))
	x509Cert, _ := x509.ParseCertificate(cert.Certificate[0])
	if !x509Cert.NotBefore.Equal(start) {
		t.Fatalf("expected NotBefore=%v, got %v", start, x509Cert.NotBefore)
	}
	duration := x509Cert.NotAfter.Sub(x509Cert.NotBefore)
	if duration != validity {
		t.Fatalf("expected validity %v, got %v", validity, duration)
	}
}

func TestSANWithIPAddress(t *testing.T) {
	cert := NewRandomTLSKeyPair(WithCommonName("192.168.1.1"))
	x509Cert, _ := x509.ParseCertificate(cert.Certificate[0])
	if len(x509Cert.IPAddresses) == 0 {
		t.Fatal("expected IP SAN")
	}
	if x509Cert.IPAddresses[0].String() != "192.168.1.1" {
		t.Fatalf("expected IP 192.168.1.1, got %s", x509Cert.IPAddresses[0])
	}
}

func TestExtKeyUsageIncludesClientAuth(t *testing.T) {
	cert := NewRandomTLSKeyPair()
	x509Cert, _ := x509.ParseCertificate(cert.Certificate[0])
	hasServerAuth := false
	hasClientAuth := false
	for _, usage := range x509Cert.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth {
			hasServerAuth = true
		}
		if usage == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	if !hasServerAuth {
		t.Error("missing ServerAuth")
	}
	if !hasClientAuth {
		t.Error("missing ClientAuth")
	}
}
