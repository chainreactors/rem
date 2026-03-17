//go:build !tinygo

package xtls

import (
	"crypto/x509/pkix"
	"fmt"
	"math/rand"
	"time"
)

// CertOption configures certificate generation.
type CertOption func(*certConfig)

type certConfig struct {
	commonName string
	subject    *pkix.Name
	keySize    int
	validFor   time.Duration
}

func WithCommonName(cn string) CertOption {
	return func(c *certConfig) {
		c.commonName = cn
	}
}

func WithSubject(subject *pkix.Name) CertOption {
	return func(c *certConfig) {
		c.subject = subject
	}
}

func WithKeySize(bits int) CertOption {
	return func(c *certConfig) {
		c.keySize = bits
	}
}

func WithValidity(d time.Duration) CertOption {
	return func(c *certConfig) {
		c.validFor = d
	}
}

// Word lists for random domain/organization generation.
var adjectives = []string{
	"swift", "bright", "cloud", "cyber", "data", "deep", "edge", "fast",
	"global", "hyper", "iron", "keen", "lunar", "mega", "net", "open",
	"prime", "quantum", "rapid", "secure", "true", "ultra", "vital", "wave",
	"apex", "blue", "core", "delta", "echo", "flex", "grid", "high",
	"intel", "just", "kilo", "link", "micro", "nova", "orbit", "pulse",
	"quest", "relay", "sigma", "tech", "unit", "vex", "wise", "xeno",
	"yield", "zero",
}

var nouns = []string{
	"systems", "solutions", "networks", "dynamics", "logic", "forge", "stack",
	"bridge", "shield", "vault", "stream", "works", "craft", "scale", "point",
	"labs", "hub", "base", "gate", "node", "link", "path", "wire", "mesh",
	"cloud", "core", "data", "flow", "grid", "host", "mind", "port",
	"soft", "tech", "view", "wave", "zone", "byte", "chip", "code",
	"disk", "edge", "fire", "hawk", "iris", "jade", "kite", "leaf",
	"mars", "nest", "onyx", "pine",
}

var tlds = []string{"com", "net", "org", "io", "co", "dev"}

var orgSuffixes = []string{
	"Co", "LLC", "Inc", "Corp", "Ltd", "Group", "Holdings", "Partners",
	"Enterprises", "Technologies", "Solutions", "Services", "Industries",
	"Consulting", "Associates", "International", "Ventures", "Digital",
	"Global", "Labs",
}

type geoEntry struct {
	country  string
	province string
	locality string
}

var geoData = []geoEntry{
	{"US", "California", "San Francisco"},
	{"US", "California", "San Jose"},
	{"US", "California", "Los Angeles"},
	{"US", "New York", "New York"},
	{"US", "New York", "Buffalo"},
	{"US", "Texas", "Austin"},
	{"US", "Texas", "Houston"},
	{"US", "Washington", "Seattle"},
	{"CA", "Ontario", "Toronto"},
	{"CA", "Ontario", "Ottawa"},
	{"CA", "British Columbia", "Vancouver"},
	{"JP", "Tokyo", "Chiyoda"},
}

func randomSubject(cn string) pkix.Name {
	if cn == "" {
		cn = randomCommonName()
	}

	geo := geoData[rand.Intn(len(geoData))]
	org := randomOrganization()

	name := pkix.Name{
		CommonName:   cn,
		Organization: org,
		Country:      []string{geo.country},
		Province:     []string{geo.province},
		Locality:     []string{geo.locality},
	}

	if rand.Intn(5) == 0 {
		name.PostalCode = []string{randomPostalCode(geo.country)}
	}

	return name
}

func randomCommonName() string {
	adj := adjectives[rand.Intn(len(adjectives))]
	noun := nouns[rand.Intn(len(nouns))]
	tld := tlds[rand.Intn(len(tlds))]
	return fmt.Sprintf("%s-%s.%s", adj, noun, tld)
}

func randomOrganization() []string {
	adj := adjectives[rand.Intn(len(adjectives))]
	noun := nouns[rand.Intn(len(nouns))]
	suffix := orgSuffixes[rand.Intn(len(orgSuffixes))]

	// Title case
	adjTitle := titleCase(adj)
	nounTitle := titleCase(noun)

	formats := []string{
		fmt.Sprintf("%s %s %s", adjTitle, nounTitle, suffix),
		fmt.Sprintf("%s%s %s", adjTitle, nounTitle, suffix),
		fmt.Sprintf("%s %s", adjTitle, suffix),
		fmt.Sprintf("%s %s", nounTitle, suffix),
		fmt.Sprintf("%s%s", adjTitle, nounTitle),
		fmt.Sprintf("%s & %s %s", adjTitle, nounTitle, suffix),
		fmt.Sprintf("%s %s %s", adjTitle, nounTitle, suffix),
		fmt.Sprintf("The %s %s", nounTitle, suffix),
	}

	return []string{formats[rand.Intn(len(formats))]}
}

func titleCase(s string) string {
	if len(s) == 0 {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b)
}

func randomPostalCode(country string) string {
	switch country {
	case "US":
		return fmt.Sprintf("%05d", rand.Intn(100000))
	case "CA":
		letters := "ABCEGHJKLMNPRSTVXY"
		return fmt.Sprintf("%c%d%c %d%c%d",
			letters[rand.Intn(len(letters))],
			rand.Intn(10),
			'A'+byte(rand.Intn(26)),
			rand.Intn(10),
			'A'+byte(rand.Intn(26)),
			rand.Intn(10),
		)
	default:
		return fmt.Sprintf("%d", 100+rand.Intn(900))
	}
}

func randomKeySize() int {
	if rand.Intn(2) == 0 {
		return 2048
	}
	return 4096
}

func randomValidFor() time.Duration {
	// 1-3 years
	days := 365 + rand.Intn(730)
	return time.Duration(days) * 24 * time.Hour
}
