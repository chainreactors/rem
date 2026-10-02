module github.com/chainreactors/rem

go 1.24.0

toolchain go1.24.3

require (
	github.com/Microsoft/go-winio v0.6.0
	github.com/chainreactors/logs v0.0.0-20250312104344-9f30fa69d3c9
	github.com/chainreactors/proxyclient v1.0.4-0.20260218115902-74a84a4535b0
	github.com/chainreactors/utils/cert v0.0.0-20260624181253-2b3d0b35862f
	github.com/golang/snappy v1.0.0
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51
	github.com/klauspost/reedsolomon v1.12.0
	github.com/miekg/dns v1.1.62
	github.com/pkg/errors v0.9.1
	github.com/shadowsocks/go-shadowsocks2 v0.1.5
	github.com/sirupsen/logrus v1.9.3
	github.com/stretchr/testify v1.10.0
	github.com/templexxx/xorsimd v0.4.3
	github.com/tjfoc/gmsm v1.4.1
	github.com/xtaci/lossyconn v0.0.0-20190602105132-8df528c0c9ae
	golang.zx2c4.com/wireguard v0.0.0-20250521234502-f333402bd9cb
	golang.zx2c4.com/wireguard/wgctrl v0.0.0-20241231184526-a9ab2273dd10
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c
)

// compatibility
require (
	golang.org/x/crypto v0.38.0
	golang.org/x/net v0.40.0
	golang.org/x/sys v0.33.0 // indirect
	golang.org/x/text v0.25.0 // indirect
)

require (
	github.com/chainreactors/files v0.0.0-20231102192550-a652458cee26 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/klauspost/cpuid/v2 v2.2.6 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/riobard/go-bloom v0.0.0-20200614022211-cdc8013cb5b3 // indirect
	github.com/templexxx/cpu v0.1.1 // indirect
	golang.org/x/mod v0.21.0 // indirect
	golang.org/x/sync v0.14.0 // indirect
	golang.org/x/time v0.7.0 // indirect
	golang.org/x/tools v0.26.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gopkg.in/check.v1 v1.0.0-20180628173108-788fd7840127 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
