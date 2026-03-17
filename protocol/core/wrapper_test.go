package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/chainreactors/rem/x/cryptor"
)

func TestPKCS7PaddingKey(t *testing.T) {
	key := DefaultKey
	padded := cryptor.PKCS7Padding([]byte(key), 16)
	t.Logf("DefaultKey: %q (len=%d)", key, len(key))
	t.Logf("Padded key: %v (len=%d)", padded, len(padded))

	// AES requires 16, 24, or 32 byte keys
	if len(padded) != 16 && len(padded) != 24 && len(padded) != 32 {
		t.Errorf("padded key length %d is not valid for AES", len(padded))
	}
}

func TestAesEncryptDecryptRoundTrip(t *testing.T) {
	key := cryptor.PKCS7Padding([]byte(DefaultKey), 16)
	plaintext := []byte(`[{"name":"cryptor","options":{"algo":"aes","key":"abcdefghijklmnopqrstuvwxyz012345","iv":"abcdefghijklmnop"}}]`)

	t.Logf("plaintext: %s", plaintext)
	t.Logf("key (len=%d): %v", len(key), key)

	encrypted, err := cryptor.AesEncrypt(plaintext, key)
	if err != nil {
		t.Fatalf("AesEncrypt failed: %v", err)
	}
	t.Logf("encrypted len: %d", len(encrypted))

	decrypted, err := cryptor.AesDecrypt(encrypted, key)
	if err != nil {
		t.Fatalf("AesDecrypt failed: %v", err)
	}
	t.Logf("decrypted: %s", decrypted)

	if string(decrypted) != string(plaintext) {
		t.Errorf("round-trip mismatch:\n  got:  %q\n  want: %q", decrypted, plaintext)
	}
}

func TestWrapperOptionsJsonMarshal(t *testing.T) {
	opts := WrapperOptions{
		&WrapperOption{
			Name:    CryptorWrapper,
			Options: map[string]string{"algo": "aes", "key": "testkey123", "iv": "testiv1234567890"},
		},
		&WrapperOption{
			Name:    CryptorWrapper,
			Options: map[string]string{"algo": "xor", "key": "xorkey", "iv": "xoriv"},
		},
	}

	data, err := json.Marshal(opts)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	t.Logf("marshaled JSON: %s", data)

	var parsed WrapperOptions
	err = json.Unmarshal(data, &parsed)
	if err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	t.Logf("parsed: %+v", parsed)

	if len(parsed) != len(opts) {
		t.Fatalf("length mismatch: got %d, want %d", len(parsed), len(opts))
	}
	for i := range opts {
		if parsed[i].Name != opts[i].Name {
			t.Errorf("[%d] name mismatch: got %q, want %q", i, parsed[i].Name, opts[i].Name)
		}
		for k, v := range opts[i].Options {
			if parsed[i].Options[k] != v {
				t.Errorf("[%d] option %q mismatch: got %q, want %q", i, k, parsed[i].Options[k], v)
			}
		}
	}
}

func TestWrapperOptionsStringAndParse(t *testing.T) {
	key := DefaultKey

	opts := WrapperOptions{
		&WrapperOption{
			Name:    CryptorWrapper,
			Options: map[string]string{"algo": "aes", "key": "abcdefghijklmnopqrstuvwxyz012345", "iv": "abcdefghijklmnop"},
		},
		&WrapperOption{
			Name:    CryptorWrapper,
			Options: map[string]string{"algo": "xor", "key": "xorkey12345678901234567890123456", "iv": "xorivxorivxorivx"},
		},
	}

	// Step 1: Serialize
	encoded := opts.String(key)
	if encoded == "" {
		t.Fatal("String() returned empty string")
	}
	t.Logf("encoded: %s", encoded)

	// Step 2: Verify base64 is valid
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}
	t.Logf("base64 decoded len: %d", len(decoded))

	// Step 3: Verify AES decrypt works
	aesKey := cryptor.PKCS7Padding([]byte(key), 16)
	decrypted, err := cryptor.AesDecrypt(decoded, aesKey)
	if err != nil {
		t.Fatalf("AesDecrypt failed: %v", err)
	}
	t.Logf("decrypted JSON: %s", decrypted)

	// Step 4: Verify JSON unmarshal works
	var parsedManual WrapperOptions
	err = json.Unmarshal(decrypted, &parsedManual)
	if err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	t.Logf("manual parsed: %+v", parsedManual)

	// Step 5: Full round-trip via ParseWrapperOptions
	parsed, err := ParseWrapperOptions(encoded, key)
	if err != nil {
		t.Fatalf("ParseWrapperOptions failed: %v", err)
	}

	if len(parsed) != len(opts) {
		t.Fatalf("round-trip length mismatch: got %d, want %d", len(parsed), len(opts))
	}
	for i := range opts {
		if parsed[i].Name != opts[i].Name {
			t.Errorf("[%d] name mismatch: got %q, want %q", i, parsed[i].Name, opts[i].Name)
		}
		for k, v := range opts[i].Options {
			if parsed[i].Options[k] != v {
				t.Errorf("[%d] option %q mismatch: got %q, want %q", i, k, parsed[i].Options[k], v)
			}
		}
	}
	t.Log("full round-trip OK")
}

func TestWrapperOptionsURLQueryRoundTrip(t *testing.T) {
	key := DefaultKey

	opts := WrapperOptions{
		&WrapperOption{
			Name:    CryptorWrapper,
			Options: map[string]string{"algo": "aes", "key": "abcdefghijklmnopqrstuvwxyz012345", "iv": "abcdefghijklmnop"},
		},
	}

	encoded := opts.String(key)
	t.Logf("original encoded: %s", encoded)

	// Simulate SetQuery -> URL String -> url.Parse -> GetQuery
	u, err := NewConsoleURL("tcp://0.0.0.0:34996")
	if err != nil {
		t.Fatalf("NewConsoleURL failed: %v", err)
	}

	u.SetQuery("wrapper", encoded)
	t.Logf("RawQuery after SetQuery: %s", u.RawQuery)

	// Serialize URL to string (as server would send to client)
	urlStr := u.String()
	t.Logf("URL string: %s", urlStr)

	// Parse URL back (as client would receive)
	u2, err := NewConsoleURL(urlStr)
	if err != nil {
		t.Fatalf("NewConsoleURL (re-parse) failed: %v", err)
	}
	t.Logf("RawQuery after re-parse: %s", u2.RawQuery)

	// Extract wrapper string
	wrapperStr := u2.GetQuery("wrapper")
	t.Logf("GetQuery wrapper: %s", wrapperStr)

	if wrapperStr != encoded {
		t.Errorf("wrapper string changed through URL round-trip:\n  original: %s\n  got:      %s", encoded, wrapperStr)
	}

	// Parse wrapper options from the extracted string
	parsed, err := ParseWrapperOptions(wrapperStr, key)
	if err != nil {
		t.Fatalf("ParseWrapperOptions failed after URL round-trip: %v", err)
	}

	if len(parsed) != len(opts) {
		t.Fatalf("length mismatch: got %d, want %d", len(parsed), len(opts))
	}
	if parsed[0].Name != opts[0].Name {
		t.Errorf("name mismatch: got %q, want %q", parsed[0].Name, opts[0].Name)
	}
	t.Log("URL query round-trip OK")
}

func TestGenerateRandomWrapperOptions(t *testing.T) {
	// Need AvailableWrappers to be populated
	if len(AvailableWrappers) == 0 {
		// Manually set for test since init() from wrapper package won't run
		AvailableWrappers = []string{CryptorWrapper}
		defer func() { AvailableWrappers = nil }()
	}

	key := DefaultKey
	opts := GenerateRandomWrapperOptions(2, 4)
	t.Logf("generated %d wrapper options", len(opts))
	for i, opt := range opts {
		t.Logf("  [%d] name=%s options=%v", i, opt.Name, opt.Options)
	}

	if len(opts) < 2 || len(opts) > 4 {
		t.Fatalf("expected 2-4 options, got %d", len(opts))
	}

	// Test round-trip with generated options
	encoded := opts.String(key)
	if encoded == "" {
		t.Fatal("String() returned empty for generated options")
	}
	t.Logf("encoded: %s", encoded)

	parsed, err := ParseWrapperOptions(encoded, key)
	if err != nil {
		t.Fatalf("ParseWrapperOptions failed for generated options: %v", err)
	}

	if len(parsed) != len(opts) {
		t.Fatalf("round-trip length mismatch: got %d, want %d", len(parsed), len(opts))
	}

	for i := range opts {
		if parsed[i].Name != opts[i].Name {
			t.Errorf("[%d] name mismatch: got %q, want %q", i, parsed[i].Name, opts[i].Name)
		}
		for k, v := range opts[i].Options {
			if parsed[i].Options[k] != v {
				t.Errorf("[%d] option %q: got %q, want %q", i, k, parsed[i].Options[k], v)
			}
		}
	}
	fmt.Println("generated options round-trip OK")
}
