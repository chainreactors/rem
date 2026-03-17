//go:build !tinygo

package xtls

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"strings"
	"testing"
	"time"
)

func parseCert(t *testing.T, cert *x509.Certificate) {
	t.Helper()
	if len(cert.Subject.Organization) == 0 || cert.Subject.Organization[0] == "" {
		t.Error("Organization is empty")
	}
	if len(cert.Subject.Country) == 0 || cert.Subject.Country[0] == "" {
		t.Error("Country is empty")
	}
	if len(cert.Subject.Province) == 0 || cert.Subject.Province[0] == "" {
		t.Error("Province is empty")
	}
	if len(cert.Subject.Locality) == 0 || cert.Subject.Locality[0] == "" {
		t.Error("Locality is empty")
	}
	if cert.Subject.CommonName == "" {
		t.Error("CommonName is empty")
	}
}

func TestRandomSubjectCompleteness(t *testing.T) {
	for i := 0; i < 20; i++ {
		subject := randomSubject("")
		if len(subject.Organization) == 0 || subject.Organization[0] == "" {
			t.Fatal("Organization is empty")
		}
		if len(subject.Country) == 0 || subject.Country[0] == "" {
			t.Fatal("Country is empty")
		}
		if len(subject.Province) == 0 || subject.Province[0] == "" {
			t.Fatal("Province is empty")
		}
		if len(subject.Locality) == 0 || subject.Locality[0] == "" {
			t.Fatal("Locality is empty")
		}
		if subject.CommonName == "" {
			t.Fatal("CommonName is empty")
		}
	}
}

func TestRandomCommonNameFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		cn := randomCommonName()
		// Must contain exactly one dot (tld separator)
		parts := strings.SplitN(cn, ".", 2)
		if len(parts) != 2 {
			t.Fatalf("CN %q does not have tld", cn)
		}
		// First part must contain a hyphen (adj-noun)
		if !strings.Contains(parts[0], "-") {
			t.Fatalf("CN %q missing hyphen in domain", cn)
		}
		// TLD should be valid
		validTLDs := map[string]bool{"com": true, "net": true, "org": true, "io": true, "co": true, "dev": true}
		if !validTLDs[parts[1]] {
			t.Fatalf("CN %q has invalid TLD %q", cn, parts[1])
		}
	}
}

func TestRandomCertUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		cn := randomCommonName()
		if seen[cn] {
			t.Fatalf("duplicate CN after %d iterations: %s", i, cn)
		}
		seen[cn] = true
	}
}

func TestRandomKeySizeDistribution(t *testing.T) {
	counts := map[int]int{}
	for i := 0; i < 100; i++ {
		size := randomKeySize()
		if size != 2048 && size != 4096 {
			t.Fatalf("unexpected key size: %d", size)
		}
		counts[size]++
	}
	if counts[2048] == 0 {
		t.Error("2048 never selected")
	}
	if counts[4096] == 0 {
		t.Error("4096 never selected")
	}
}

func TestRandomValidForRange(t *testing.T) {
	minDur := 365 * 24 * time.Hour
	maxDur := 1095 * 24 * time.Hour // 365 + 730 = 1095 days
	for i := 0; i < 100; i++ {
		d := randomValidFor()
		if d < minDur || d > maxDur {
			t.Fatalf("validity %v out of range [%v, %v]", d, minDur, maxDur)
		}
	}
}

func TestNewRandomTLSKeyPairBackwardCompat(t *testing.T) {
	cert := NewRandomTLSKeyPair()
	if cert == nil {
		t.Fatal("nil certificate")
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("no certificate data")
	}

	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	parseCert(t, x509Cert)
}

func TestWithCommonName(t *testing.T) {
	cert := NewRandomTLSKeyPair(WithCommonName("test.example.com"))
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if x509Cert.Subject.CommonName != "test.example.com" {
		t.Fatalf("expected CN test.example.com, got %s", x509Cert.Subject.CommonName)
	}
	// SAN should include the CN as DNS name
	if len(x509Cert.DNSNames) == 0 || x509Cert.DNSNames[0] != "test.example.com" {
		t.Fatalf("expected SAN DNS name test.example.com, got %v", x509Cert.DNSNames)
	}
}

func TestWithSubject(t *testing.T) {
	subject := &pkix.Name{
		CommonName:   "custom.test",
		Organization: []string{"Custom Org"},
		Country:      []string{"DE"},
		Province:     []string{"Bavaria"},
		Locality:     []string{"Munich"},
	}
	cert := NewRandomTLSKeyPair(WithSubject(subject))
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if x509Cert.Subject.CommonName != "custom.test" {
		t.Fatalf("expected CN custom.test, got %s", x509Cert.Subject.CommonName)
	}
	if x509Cert.Subject.Organization[0] != "Custom Org" {
		t.Fatalf("expected org Custom Org, got %s", x509Cert.Subject.Organization[0])
	}
}

func TestWithKeySize(t *testing.T) {
	cert := NewRandomTLSKeyPair(WithKeySize(4096))
	if cert == nil {
		t.Fatal("nil certificate")
	}
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if x509Cert.PublicKeyAlgorithm.String() != "RSA" {
		t.Fatal("not RSA")
	}
	// 4096-bit RSA key has a public key of 512 bytes (+ overhead)
	if x509Cert.PublicKey == nil {
		t.Fatal("nil public key")
	}
}

func TestWithValidity(t *testing.T) {
	validity := 180 * 24 * time.Hour // 180 days
	cert := NewRandomTLSKeyPair(WithValidity(validity))
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	duration := x509Cert.NotAfter.Sub(x509Cert.NotBefore)
	if duration != validity {
		t.Fatalf("expected validity %v, got %v", validity, duration)
	}
}

func TestSANWithIPAddress(t *testing.T) {
	cert := NewRandomTLSKeyPair(WithCommonName("192.168.1.1"))
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(x509Cert.IPAddresses) == 0 {
		t.Fatal("expected IP SAN")
	}
	if x509Cert.IPAddresses[0].String() != "192.168.1.1" {
		t.Fatalf("expected IP 192.168.1.1, got %s", x509Cert.IPAddresses[0])
	}
}

func TestSANWithDNSName(t *testing.T) {
	cert := NewRandomTLSKeyPair()
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	// Random CN should produce a DNS SAN
	if len(x509Cert.DNSNames) == 0 {
		t.Fatal("expected DNS SAN for random CN")
	}
	if x509Cert.DNSNames[0] != x509Cert.Subject.CommonName {
		t.Fatalf("DNS SAN %q != CN %q", x509Cert.DNSNames[0], x509Cert.Subject.CommonName)
	}
}

func TestExtKeyUsageIncludesClientAuth(t *testing.T) {
	cert := NewRandomTLSKeyPair()
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
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

func TestRandomOrganization(t *testing.T) {
	for i := 0; i < 50; i++ {
		org := randomOrganization()
		if len(org) == 0 || org[0] == "" {
			t.Fatal("empty organization")
		}
	}
}

func TestRandomPostalCode(t *testing.T) {
	us := randomPostalCode("US")
	if len(us) != 5 {
		t.Fatalf("US postal code should be 5 digits, got %q", us)
	}

	ca := randomPostalCode("CA")
	if len(ca) != 7 { // "X0X 0X0"
		t.Fatalf("CA postal code should be 7 chars, got %q (len %d)", ca, len(ca))
	}
}
