package wrapper

import (
	"io"

	"github.com/chainreactors/rem/protocol/core"
)

func init() {
	core.WrapperRegister(core.CryptorWrapper, func(r io.Reader, w io.Writer, opt map[string]string) (core.Wrapper, error) {
		return NewCryptorWrapper(r, w, opt)
	})
	core.AvailableWrappers = append(core.AvailableWrappers, core.CryptorWrapper)
}

func NewCryptorWrapper(r io.Reader, w io.Writer, opt map[string]string) (core.Wrapper, error) {
	return newStreamBlockWrapper(core.CryptorWrapper, "aes", make([]byte, 32), r, w, opt)
}
