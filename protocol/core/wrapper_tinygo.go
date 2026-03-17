//go:build tinygo

package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"sync"

	"github.com/chainreactors/rem/x/utils"
)

var AvailableWrappers []string

func ParseWrapperOptions(s string, key string) (WrapperOptions, error) {
	decodedData, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}

	var options WrapperOptions
	err = json.Unmarshal(decodedData, &options)
	if err != nil {
		return nil, err
	}

	return options, nil
}

type WrapperOptions []*WrapperOption

func (opts WrapperOptions) String(key string) string {
	marshal, err := json.Marshal(opts)
	if err != nil {
		utils.Log.Error(err.Error())
		return ""
	}
	return base64.StdEncoding.EncodeToString(marshal)
}

type WrapperOption struct {
	Name    string            `json:"name"`
	Options map[string]string `json:"options"`
}

type Wrapper interface {
	Name() string
	io.ReadWriteCloser
}

type WrapperCreatorFn func(r io.Reader, w io.Writer, opt map[string]string) (Wrapper, error)

var wrapperCreators sync.Map // map[string]WrapperCreatorFn

func WrapperRegister(name string, fn WrapperCreatorFn) {
	if _, loaded := wrapperCreators.LoadOrStore(name, fn); loaded {
		wrapperCreators.Store(name, fn)
	}
}

func WrapperCreate(name string, r io.Reader, w io.Writer, opt map[string]string) (Wrapper, error) {
	if fn, ok := wrapperCreators.Load(name); ok {
		return fn.(WrapperCreatorFn)(r, w, opt)
	}
	available := GetRegisteredWrappers()
	return nil, fmt.Errorf("wrapper [%s] is not registered. Available wrappers: %v", name, available)
}

func GetRegisteredWrappers() []string {
	var names []string
	wrapperCreators.Range(func(key, value interface{}) bool {
		names = append(names, key.(string))
		return true
	})
	return names
}

func GenerateRandomWrapperOption() *WrapperOption {
	if len(AvailableWrappers) == 0 {
		return nil
	}

	name := AvailableWrappers[rand.Intn(len(AvailableWrappers))]
	opt := &WrapperOption{
		Name:    name,
		Options: make(map[string]string),
	}
	return opt
}

func GenerateRandomWrapperOptions(minCount, maxCount int) WrapperOptions {
	if minCount < 1 {
		minCount = 1
	}
	if maxCount < minCount {
		maxCount = minCount
	}
	if len(AvailableWrappers) == 0 {
		return nil
	}

	count := minCount
	if maxCount > minCount {
		count += rand.Intn(maxCount - minCount + 1)
	}

	var opts WrapperOptions
	for i := 0; i < count; i++ {
		opt := GenerateRandomWrapperOption()
		if opt == nil {
			break
		}
		opts = append(opts, opt)
	}

	rand.Shuffle(len(opts), func(i, j int) {
		opts[i], opts[j] = opts[j], opts[i]
	})

	return opts
}

func (u *URL) IP() net.IP {
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	// TinyGo does not implement net.LookupIP, but net.Dial works through netdev.
	conn, err := net.Dial("tcp", net.JoinHostPort(host, "80"))
	if err != nil {
		return nil
	}
	defer conn.Close()
	if addr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		return addr.IP
	}
	return nil
}
