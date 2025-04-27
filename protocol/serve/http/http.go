package http

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	core.OutboundRegister(core.HTTPServe, NewHTTPProxyOutbound)
	core.InboundRegister(core.HTTPServe, NewHTTPProxyInbound)
}

type HTTPProxy struct {
	dial core.ContextDialer
	*core.PluginOption
	httpClient *http.Client
}

func (hp *HTTPProxy) Close() error {
	return nil
}

func NewHTTPProxyInbound(options map[string]string) (core.Inbound, error) {
	hp := &HTTPProxy{
		PluginOption: core.NewPluginOption(options, core.InboundPlugin, core.HTTPServe),
	}
	utils.Log.Importantf("[agent.inbound] http serving: %s , %s", hp.String(), hp.URL())
	return hp, nil
}

func NewHTTPProxyOutbound(opts map[string]string, dial core.ContextDialer) (core.Outbound, error) {
	hp := &HTTPProxy{
		PluginOption: core.NewPluginOption(opts, core.OutboundPlugin, core.HTTPServe),
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: dial.DialContext,
			},
		},
		dial: dial,
	}

	utils.Log.Importantf("[agent.outbound] %s", hp.URL())
	return hp, nil
}

func (hp *HTTPProxy) Name() string {
	return core.HTTPServe
}

func (hp *HTTPProxy) Handle(conn io.ReadWriteCloser, realConn net.Conn) (net.Conn, error) {
	reader := bufio.NewReader(conn)
	method, err := reader.Peek(7)
	if err != nil {
		conn.Close()
		return nil, err
	}
	wrapConn := cio.WrapReadWriteCloser(reader, conn, conn.Close)
	if strings.ToUpper(string(method)) == "CONNECT" {
		request, err := http.ReadRequest(reader)
		if err != nil {
			conn.Close()
			return nil, err
		}

		return hp.handleConnect(request, wrapConn)
	} else if strings.ToUpper(string(method)[:3]) == "GET" {
		request, err := http.ReadRequest(reader)
		if err != nil {
			conn.Close()
			return nil, err
		}
		err = hp.handlerGet(wrapConn, request)
		if err != nil {
			return nil, err
		}
		return cio.WrapConn(realConn, conn), nil
	} else {
		conn.Close()
		return nil, fmt.Errorf("unsupported method")
	}
}

func (hp *HTTPProxy) Relay(conn net.Conn, bridge io.ReadWriteCloser) (net.Conn, error) {
	reader := bufio.NewReader(conn)
	method, err := reader.Peek(7)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if strings.ToUpper(string(method)) == "CONNECT" {
		wrapConn, err := hp.handlerRelay(reader, conn, bridge)
		if err != nil {
			conn.Close()
			return nil, err
		}

		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
		return wrapConn, err
	} else {
		conn.Close()
		return nil, fmt.Errorf("unsupported method")
	}
}

func (hp *HTTPProxy) handlerRelay(reader *bufio.Reader, conn net.Conn, bridge io.ReadWriteCloser) (net.Conn, error) {
	request, err := http.ReadRequest(reader)
	if err != nil {
		return nil, err
	}
	rwc := cio.WrapReadWriteCloser(reader, conn, conn.Close)
	if ok := hp.Auth(request); !ok {
		res := build407Response()
		_ = res.Write(conn)
		if res.Body != nil {
			res.Body.Close()
		}
		return nil, fmt.Errorf("auth failed")
	}

	// 修复请求主机的端口号
	u, err := core.NewURL(request.URL.Scheme + "://" + request.URL.Host)
	if err != nil {
		res := build400Response()
		_ = res.Write(conn)
		if res.Body != nil {
			res.Body.Close()
		}
		return nil, fmt.Errorf("host invalid")
	}
	u.FixPort()

	req, err := socks5.NewRelay(u.Host)
	if err != nil {
		res := build400Response()
		_ = res.Write(conn)
		if res.Body != nil {
			res.Body.Close()
		}
		return nil, fmt.Errorf("host invalid")
	}
	_, err = bridge.Write(req.BuildRelay())
	if err != nil {
		return nil, err
	}
	_, _, err = socks5.ReadReply(bridge)
	if err != nil {
		return nil, err
	}

	return cio.WrapConn(conn, rwc), nil
}

func (hp *HTTPProxy) handlerGet(rw io.ReadWriteCloser, req *http.Request) error {
	if ok := hp.Auth(req); !ok {
		res := build407Response()
		_ = res.Write(rw)
		if res.Body != nil {
			res.Body.Close()
		}
		return fmt.Errorf("auth failed")
	}

	removeProxyHeaders(req)

	resp, err := hp.httpClient.Transport.RoundTrip(req)
	if err != nil {
		res := build400Response()
		_ = res.Write(rw)
		if res.Body != nil {
			res.Body.Close()
		}
		return err
	}

	defer resp.Body.Close()

	var header bytes.Buffer
	statusLine := fmt.Sprintf("HTTP/%d.%d %d %s\r\n", resp.ProtoMajor, resp.ProtoMinor, resp.StatusCode, http.StatusText(resp.StatusCode))
	header.Write([]byte(statusLine))

	// 构建并发送响应头, 复制响应头��客户端
	for key, values := range resp.Header {
		for _, value := range values {
			header.Write([]byte(fmt.Sprintf("%s: %s\r\n", key, value)))
		}
	}

	header.Write([]byte("\r\n"))
	_, err = rw.Write(header.Bytes())
	if err != nil {
		return err
	}

	_, err = io.Copy(rw, resp.Body)
	if err != nil && err != io.EOF {
		return err
	}
	return nil
}

func (hp *HTTPProxy) Auth(req *http.Request) bool {
	if hp.Proxy.Username == "" && hp.Proxy.Password == "" {
		return true
	}

	s := strings.SplitN(req.Header.Get("Proxy-Authorization"), " ", 2)
	if len(s) != 2 {
		return false
	}

	b, err := base64.StdEncoding.DecodeString(s[1])
	if err != nil {
		return false
	}

	pair := strings.SplitN(string(b), ":", 2)
	if len(pair) != 2 {
		return false
	}

	if hp.Proxy.Username != pair[0] || hp.Proxy.Password != pair[1] {
		time.Sleep(200 * time.Millisecond)
		return false
	}
	return true
}

func (hp *HTTPProxy) handleConnect(req *http.Request, rwc io.ReadWriteCloser) (net.Conn, error) {
	defer rwc.Close()
	if ok := hp.Auth(req); !ok {
		res := build407Response()
		_ = res.Write(rwc)
		if res.Body != nil {
			res.Body.Close()
		}
		return nil, fmt.Errorf("auth failed")
	}

	// 修复请求主机的端口号
	u, err := core.NewURL(req.URL.String())
	if err != nil {
		res := build400Response()
		_ = res.Write(rwc)
		return nil, fmt.Errorf("host invalid")
	}
	u.FixPort()

	remote, err := hp.dial.Dial("tcp", u.Host)
	if err != nil {
		res := &http.Response{
			StatusCode: 400,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
		}
		_ = res.Write(rwc)
		return nil, err
	}
	_, err = rwc.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	if err != nil {
		return nil, err
	}
	return remote, nil
}

func removeProxyHeaders(req *http.Request) {
	req.RequestURI = ""
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Connection")
	req.Header.Del("Proxy-Authenticate")
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("TE")
	req.Header.Del("Trailers")
	req.Header.Del("Transfer-Encoding")
	req.Header.Del("Upgrade")
}

func build407Response() *http.Response {
	header := make(map[string][]string)
	header["Proxy-Authenticate"] = []string{"Basic"}
	header["Connection"] = []string{"close"}
	res := &http.Response{
		Status:     "407 Not authorized",
		StatusCode: 407,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     header,
	}
	return res
}

func build400Response() *http.Response {
	res := &http.Response{
		StatusCode: 400,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
	}
	return res
}
