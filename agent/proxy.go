package agent

import (
	"encoding/hex"

	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/cryptor"
)

func (agent *Agent) initProxyClient() error {
	if len(agent.Proxies) == 0 {
		agent.client = &core.NetDialer{}
		return nil
	}
	client, err := proxyclient.NewClientChain(agent.Proxies)
	if err != nil {
		return err
	}
	agent.client = core.NewProxyDialer(client)
	return nil
}

func buildAuthToken(key []byte) (string, error) {
	token, err := cryptor.AesEncrypt(key, cryptor.PKCS7Padding(key, 16))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(token), nil
}
