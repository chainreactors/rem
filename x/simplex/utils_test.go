package simplex

import (
	"testing"
)

func TestValidateSeed(t *testing.T) {
	tests := []struct {
		name    string
		seed    string
		wantErr bool
	}{
		{"valid lowercase", "abcd", false},
		{"valid uppercase", "ABCD", false},
		{"valid mixed", "aBcD1234", false},
		{"valid 32 chars", "abcdefghijklmnopqrstuvwxyz123456", false},
		{"too short", "abc", true},
		{"too long", "abcdefghijklmnopqrstuvwxyz1234567", true},
		{"empty", "", true},
		{"contains underscore", "ab_cd", true},
		{"contains dash", "ab-cd", true},
		{"contains space", "ab cd", true},
		{"contains dot", "ab.cd", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSeed(tt.seed)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSeed(%q) error = %v, wantErr %v", tt.seed, err, tt.wantErr)
			}
		})
	}
}

func TestFormatSeedTimestamp(t *testing.T) {
	result := formatSeedTimestamp("abcd1234", 1710374400)
	if result != "abcd1234_1710374400" {
		t.Errorf("formatSeedTimestamp got %q, want %q", result, "abcd1234_1710374400")
	}
}

func TestParseSeedTimestamp(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		wantSeed string
		wantTS   int64
		wantErr  bool
	}{
		{"normal", "abcd1234_1710374400", "abcd1234", 1710374400, false},
		{"seed with underscore", "ab_cd_1710374400", "ab_cd", 1710374400, false},
		{"no underscore", "abcd1234", "", 0, true},
		{"empty seed", "_1710374400", "", 0, true},
		{"bad timestamp", "abcd_notanumber", "", 0, true},
		{"empty string", "", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seed, ts, err := parseSeedTimestamp(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSeedTimestamp(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if seed != tt.wantSeed {
					t.Errorf("seed = %q, want %q", seed, tt.wantSeed)
				}
				if ts != tt.wantTS {
					t.Errorf("ts = %d, want %d", ts, tt.wantTS)
				}
			}
		})
	}
}

func TestParseSeedTimestamp_Roundtrip(t *testing.T) {
	seed := "testSeed1234"
	var ts int64 = 1710374400
	formatted := formatSeedTimestamp(seed, ts)
	gotSeed, gotTS, err := parseSeedTimestamp(formatted)
	if err != nil {
		t.Fatalf("parseSeedTimestamp(%q) unexpected error: %v", formatted, err)
	}
	if gotSeed != seed || gotTS != ts {
		t.Errorf("roundtrip failed: got (%q, %d), want (%q, %d)", gotSeed, gotTS, seed, ts)
	}
}
