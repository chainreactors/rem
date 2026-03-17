//go:build !tinygo

package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"sync"

	"github.com/chainreactors/rem/x/cryptor"
	"github.com/chainreactors/rem/x/utils"
)

var AvailableWrappers []string

func ParseWrapperOptions(s string, key string) (WrapperOptions, error) {
	decodedData, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}

	decryptedData, err := cryptor.AesDecrypt(decodedData, cryptor.PKCS7Padding([]byte(key), 16))
	if err != nil {
		return nil, err
	}

	var options WrapperOptions
	err = json.Unmarshal(decryptedData, &options)
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
	data, err := cryptor.AesEncrypt(marshal, cryptor.PKCS7Padding([]byte(key), 16))
	if err != nil {
		utils.Log.Error(err.Error())
		return ""
	}

	return base64.StdEncoding.EncodeToString(data)
}

// WrapperOption 定义wrapper的基本配置结构
type WrapperOption struct {
	Name    string            `json:"name"`
	Options map[string]string `json:"options"`
}

// Wrapper 定义了数据包装器的基本接口
type Wrapper interface {
	Name() string
	io.ReadWriteCloser
}

// WrapperCreatorFn 是创建wrapper的工厂函数类型
type WrapperCreatorFn func(r io.Reader, w io.Writer, opt map[string]string) (Wrapper, error)

// Use sync.Map to survive TinyGo WASM package-level variable corruption.
var wrapperCreators sync.Map // map[string]WrapperCreatorFn

// WrapperRegister 注册一个wrapper类型
func WrapperRegister(name string, fn WrapperCreatorFn) {
	if _, loaded := wrapperCreators.LoadOrStore(name, fn); loaded {
		wrapperCreators.Store(name, fn)
	}
}

// WrapperCreate 创建一个指定类型的wrapper
func WrapperCreate(name string, r io.Reader, w io.Writer, opt map[string]string) (Wrapper, error) {
	if fn, ok := wrapperCreators.Load(name); ok {
		return fn.(WrapperCreatorFn)(r, w, opt)
	}
	available := GetRegisteredWrappers()
	return nil, fmt.Errorf("wrapper [%s] is not registered. Available wrappers: %v", name, available)
}

// GetRegisteredWrappers 获取所有已注册的 wrapper 名称
func GetRegisteredWrappers() []string {
	var names []string
	wrapperCreators.Range(func(key, value interface{}) bool {
		names = append(names, key.(string))
		return true
	})
	return names
}

// GenerateRandomWrapperOption 生成单个随机的WrapperOption
func GenerateRandomWrapperOption() *WrapperOption {
	name := AvailableWrappers[rand.Intn(len(AvailableWrappers))]

	opt := &WrapperOption{
		Name:    name,
		Options: make(map[string]string),
	}

	switch name {
	case CryptorWrapper:
		algo := "aes"
		if rand.Intn(2) == 1 {
			algo = "xor"
		}
		opt.Options["algo"] = algo
		opt.Options["key"] = utils.RandomString(32)
		opt.Options["iv"] = utils.RandomString(16)
	}

	return opt
}

// GenerateRandomWrapperOptions 生成随机数量、随机组合的Wrapper配置
func GenerateRandomWrapperOptions(minCount, maxCount int) WrapperOptions {
	if minCount < 1 {
		minCount = 1
	}
	if maxCount < minCount {
		maxCount = minCount
	}

	count := minCount
	if maxCount > minCount {
		count += rand.Intn(maxCount - minCount + 1)
	}

	var opts WrapperOptions
	for i := 0; i < count; i++ {
		opt := GenerateRandomWrapperOption()
		opts = append(opts, opt)
	}

	rand.Shuffle(len(opts), func(i, j int) {
		opts[i], opts[j] = opts[j], opts[i]
	})

	return opts
}
