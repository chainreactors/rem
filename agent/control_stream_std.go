//go:build !tinygo

package agent

import "net"

func (agent *Agent) setControlStream(_ net.Conn) {}
