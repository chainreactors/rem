//go:build tinygo

package agent

import "net"

func (agent *Agent) setControlStream(stream net.Conn) {
	if agent.stream0 == nil {
		agent.stream0 = stream
	}
}
