package wireguard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// genWGKeys generates a WireGuard private/public key pair as hex strings.
func genWGKeys() (privHex, pubHex string, err error) {
	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return "", "", err
	}
	pub := priv.PublicKey()
	return hex.EncodeToString(priv[:]), hex.EncodeToString(pub[:]), nil
}

// pubKeyFromPrivHex derives the public key from a hex-encoded private key.
func pubKeyFromPrivHex(privHex string) (string, error) {
	privBytes, err := hex.DecodeString(privHex)
	if err != nil {
		return "", fmt.Errorf("invalid private key hex: %w", err)
	}
	if len(privBytes) != 32 {
		return "", fmt.Errorf("private key must be 32 bytes, got %d", len(privBytes))
	}
	var key wgtypes.Key
	copy(key[:], privBytes)
	pub := key.PublicKey()
	return hex.EncodeToString(pub[:]), nil
}

// tunIPFromPubKey derives a deterministic tun IP in the 100.64.0.0/16 subnet
// from the SHA256 hash of the public key hex string. This avoids manual IP
// assignment for multi-client scenarios. The server uses 100.64.0.1, so
// hash collisions with .0.0 and .0.1 are shifted to .0.2.
func tunIPFromPubKey(pubKeyHex string) string {
	h := sha256.Sum256([]byte(pubKeyHex))
	x, y := int(h[0]), int(h[1])
	if x == 0 && y <= 1 {
		y = 2
	}
	return fmt.Sprintf("100.64.%d.%d", x, y)
}
