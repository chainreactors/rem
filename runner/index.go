package runner

// basic
import (
	_ "github.com/chainreactors/rem/protocol/wrapper"
)

// application
import (
	_ "github.com/chainreactors/rem/protocol/serve/http"
	_ "github.com/chainreactors/rem/protocol/serve/portforward"
	_ "github.com/chainreactors/rem/protocol/serve/raw"
	_ "github.com/chainreactors/rem/protocol/serve/socks"
	//_ "github.com/chainreactors/rem/protocol/serve/externalc2"
	//_ "github.com/chainreactors/rem/protocol/serve/shadowsocks"
	//_ "github.com/chainreactors/rem/protocol/serve/trojan"
)

// transport
import (
	_ "github.com/chainreactors/rem/protocol/tunnel/http"
	_ "github.com/chainreactors/rem/protocol/tunnel/tcp"
	_ "github.com/chainreactors/rem/protocol/tunnel/udp"
	//_ "github.com/chainreactors/rem/protocol/tunnel/unix"
	//_ "github.com/chainreactors/rem/protocol/tunnel/websocket"
	//_ "github.com/chainreactors/rem/protocol/tunnel/wireguard"
	//_ "github.com/chainreactors/rem/protocol/tunnel/icmp"
)
