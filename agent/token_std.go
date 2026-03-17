//go:build !tinygo

package agent

import (
	"encoding/hex"

	"github.com/chainreactors/rem/x/cryptor"
)

func buildAuthToken(key []byte) (string, error) {
	token, err := cryptor.AesEncrypt(key, cryptor.PKCS7Padding(key, 16))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(token), nil
}
