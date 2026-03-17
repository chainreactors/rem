//go:build !tinygo

package agent

import (
	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/protocol/core"
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
