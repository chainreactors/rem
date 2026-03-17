//go:build tinygo

package runner

import (
	"encoding/hex"
	"fmt"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/utils"
)

func resolveAgentUsername() string {
	return "tinygo"
}

func resolveAgentInterfaces() []string {
	return nil
}

func buildAgentName(hostname string) string {
	name := hostname + "-" + utils.RandomString(16)
	if len(name) > 24 {
		name = name[:24]
	}
	return name
}

func (c *Console) handlerSubscribe() {
}

func detectExternalIP() string {
	return "127.0.0.1"
}

func (c *Console) Bind() error {
	return fmt.Errorf("tinygo: bind mode is not supported")
}

func buildConsoleToken(key string) (string, error) {
	return hex.EncodeToString([]byte(key)), nil
}

func defaultWrapperOptions() core.WrapperOptions {
	return nil
}
