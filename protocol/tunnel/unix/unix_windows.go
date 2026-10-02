package unix

import (
	"context"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/utils"
)

func NewUnixDialer(ctx context.Context) *UnixDialer {
	return &UnixDialer{
		meta: core.GetMetas(ctx),
	}
}

func (c *UnixDialer) Dial(dst string) (net.Conn, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	c.meta["url"] = u
	host := u.Hostname()
	pipePath := `\\` + host + `\pipe\` + u.PathString()
	utils.Log.Debugf("dial pipe: %s", pipePath)
	return winio.DialPipe(pipePath, nil)
}

func NewUnixListener(ctx context.Context) *UnixListener {
	return &UnixListener{
		meta: core.GetMetas(ctx),
	}
}

func (c *UnixListener) Listen(dst string) (net.Listener, error) {
	pipeUrl, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	if pipeUrl.Hostname() == "0.0.0.0" {
		pipeUrl.Host = "."
	}
	if pipeUrl.PathString() == "" {
		pipeUrl.Path = "/" + c.meta["pipe"].(string)
	}
	pipePath := fmt.Sprintf(`\\%s\pipe\%s`, pipeUrl.Hostname(), pipeUrl.PathString())
	c.meta["url"] = pipeUrl
	config := &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;WD)", // WD 表示 Everyone，允许所有人访问
		MessageMode:        true,              // 消息模式
		InputBufferSize:    65536,             // 默认缓冲区大
		OutputBufferSize:   65536,
	}

	listener, err := winio.ListenPipe(pipePath, config)
	if err != nil {
		return nil, err
	}
	utils.Log.Debugf("listen pipe: %s", pipePath)
	c.listener = listener
	return listener, nil
}
