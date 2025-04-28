package core

import (
	"fmt"
	"github.com/chainreactors/rem/x/utils"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func NewConsoleURL(u string) (*URL, error) {
	// "" -> "tcp://0.0.0.0:34996"
	// ":8888" -> "tcp://0.0.0.0:8888"
	// "1.1.1.1" -> "tcp://1.1.1.1:34996"
	// "1.1.1.1:8888" -> "tcp://1.1.1.1:8888"
	// "udp://:8888" -> "udp://0.0.0.0:8888"
	if !strings.Contains(u, "://") {
		u = DefaultConsoleProto + "://" + u
	}

	pu, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	parsed := &URL{URL: pu}
	parsed.Scheme = strings.Replace(parsed.Scheme, "rem+", "", 1)
	parsed.RawScheme = parsed.Scheme
	parsed.Scheme = Normalize(parsed.Scheme)
	if parsed.Host == "" {
		parsed.Host = "0.0.0.0:" + DefaultConsolePort
	}
	if parsed.Hostname() == "" {
		parsed.Host = "0.0.0.0:" + parsed.Port()
	}
	if parsed.Port() == "0" || parsed.Port() == "" {
		parsed.Host = parsed.Hostname() + ":" + DefaultConsolePort
	}

	return parsed, nil
}

func NewURL(u string) (*URL, error) {
	if !strings.Contains(u, "://") {
		u = DefaultScheme + "://" + u
	}

	parsed, err := url.Parse(u)
	if err != nil {
		return nil, err
	}

	v := &URL{URL: parsed}
	if ss := strings.Split(v.Scheme, "+"); len(ss) == 1 {
		v.RawScheme = ss[0]
		v.Scheme = Normalize(ss[0])
		v.Tunnel = "tcp"
	} else {
		v.RawScheme = ss[1]
		v.Scheme = Normalize(ss[1])
		v.Tunnel = Normalize(ss[0])
	}

	if v.Host == "" {
		v.Host = "0.0.0.0:0"
	}

	if v.Hostname() == "" {
		v.Host = "0.0.0.0" + ":" + v.Port()
	}

	if v.Port() == "" {
		v.Host = fmt.Sprintf("%s:0", v.Hostname())
	}

	if v.User == nil {
		v.User = url.UserPassword(DefaultUsername, DefaultPassword)
	}
	return v, nil
}

type URLs struct {
	ConsoleURL *URL
	RemoteURL  *URL
	LocalURL   *URL
}

func (urls *URLs) Copy() *URLs {
	nurls := &URLs{}
	if urls.ConsoleURL != nil {
		nurls.ConsoleURL = urls.ConsoleURL.Copy()
	}
	if urls.RemoteURL != nil {
		nurls.RemoteURL = urls.RemoteURL.Copy()
	}
	if urls.LocalURL != nil {
		nurls.LocalURL = urls.LocalURL.Copy()
	}

	return nurls
}

type URL struct {
	*url.URL
	RawScheme string
	Tunnel    string
}

func (u *URL) Copy() *URL {
	return &URL{
		URL: &url.URL{
			Scheme:     u.Scheme,
			Opaque:     u.Opaque,
			User:       u.User,
			Host:       u.Host,
			Path:       u.Path,
			RawPath:    u.RawPath,
			ForceQuery: u.ForceQuery,
			RawQuery:   u.RawQuery,
			Fragment:   u.Fragment,
		},
		Tunnel: u.Tunnel,
	}
}

func (u *URL) IP() net.IP {
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil
	}

	return ips[0]
}

func (u *URL) PathString() string {
	return strings.TrimLeft(u.Path, "/")
}

func (u *URL) Network() string {
	return u.Scheme
}

func (u *URL) String() string {
	var buf strings.Builder
	if u.Scheme != "" {
		if u.Tunnel != "" && u.Tunnel != "tcp" {
			buf.WriteString(u.Tunnel + "+")
		}
		if u.RawScheme != "" {
			buf.WriteString(u.RawScheme)
		} else {
			buf.WriteString(u.Scheme)
		}
		buf.WriteByte(':')
	}
	if u.Opaque != "" {
		buf.WriteString(u.Opaque)
	} else {
		if u.Scheme != "" || u.Host != "" || u.User != nil {
			if u.Host == "" && u.User == nil {
				// omit empty host
			} else {
				if u.Host != "" || u.Path != "" || u.User != nil {
					buf.WriteString("//")
				}
				if ui := u.User; ui != nil {
					buf.WriteString(ui.String())
					buf.WriteByte('@')
				}
				if h := u.Host; h != "" {
					buf.WriteString(h)
				}
			}
		}
		path := u.EscapedPath()
		if path != "" && path[0] != '/' && u.Host != "" {
			buf.WriteByte('/')
		}
		if buf.Len() == 0 {
			// RFC 3986 §4.2
			// A path segment that contains a colon character (e.g., "this:that")
			// cannot be used as the first segment of a relative-path reference, as
			// it would be mistaken for a scheme name. Such a segment must be
			// preceded by a dot-segment (e.g., "./this:that") to make a relative-
			// path reference.
			if segment, _, _ := strings.Cut(path, "/"); strings.Contains(segment, ":") {
				buf.WriteString("./")
			}
		}
		buf.WriteString(path)
	}
	if u.ForceQuery || u.RawQuery != "" {
		buf.WriteByte('?')
		buf.WriteString(u.RawQuery)
	}
	if u.Fragment != "" {
		buf.WriteByte('#')
		buf.WriteString(u.EscapedFragment())
	}
	return buf.String()
}

func (u *URL) Options() map[string]string {
	opt := map[string]string{
		"username": u.Username(),
		"password": u.Password(),
		"port":     u.Port(),
	}

	for k, v := range u.Query() {
		opt[k] = v[0]
	}

	return opt
}

func (u *URL) Username() string {
	if u.User == nil {
		return ""
	}
	return u.User.Username()
}

func (u *URL) Password() string {
	if u.User == nil {
		return ""
	}
	password, _ := u.User.Password()
	return password
}

func (u *URL) IntPort() int32 {
	// string to uint16
	p, _ := strconv.Atoi(u.Port())
	return int32(p)
}
func (u *URL) SetPort(port int) {
	u.Host = fmt.Sprintf("%s:%d", u.Hostname(), port)
}

func (u *URL) SetHostname(hostname string) {
	u.Host = fmt.Sprintf("%s:%s", hostname, u.Port())
}

func (u *URL) SplitAddr() (string, int) {
	return utils.SplitAddr(u.Host)
}

// SetQuery adds or updates a query parameter in the URL.
func (u *URL) SetQuery(key, value string) {
	// Parse the existing query parameters
	queryParams := u.Query()

	// Add or update the parameter
	queryParams.Set(key, value)

	// Update the RawQuery field with the new query string
	u.RawQuery = queryParams.Encode()
}

func (u *URL) GetQuery(key string) string {
	return u.Query().Get(key)
}

func (u *URL) FixPort() {
	// 如果已经有端口号，则不需要修复
	if u.Port() != "" && u.Port() != "0" {
		return
	}

	// 根据协议设置默认端口
	switch u.Scheme {
	case "http":
		u.Host = fmt.Sprintf("%s:80", u.Hostname())
	case "https":
		u.Host = fmt.Sprintf("%s:443", u.Hostname())
	}
}
