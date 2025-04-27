module github.com/chainreactors/rem

go 1.18

require (
	github.com/Microsoft/go-winio v0.5.1
	github.com/chainreactors/go-metrics v0.0.0-20220926021830-24787b7a10f8
	github.com/chainreactors/logs v0.0.0-20250312104344-9f30fa69d3c9
	github.com/chainreactors/proxyclient v1.0.2
	github.com/golang/snappy v1.0.0
	github.com/jessevdk/go-flags v1.5.0
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51
	github.com/klauspost/reedsolomon v1.12.0
	github.com/pkg/errors v0.9.1
	github.com/shadowsocks/go-shadowsocks2 v0.1.5
	github.com/sirupsen/logrus v1.9.3
	github.com/stretchr/testify v1.10.0
	github.com/templexxx/xorsimd v0.4.3
	github.com/tjfoc/gmsm v1.4.1
	github.com/xtaci/lossyconn v0.0.0-20190602105132-8df528c0c9ae
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0
	golang.zx2c4.com/wireguard v0.0.0-20230325221338-052af4a8072b
	golang.zx2c4.com/wireguard/wgctrl v0.0.0-20230429144221-925a1e7659e6
	google.golang.org/protobuf v1.33.0
	gopkg.in/yaml.v3 v3.0.1
)

// compatibility
require (
	golang.org/x/crypto v0.33.0
	golang.org/x/exp v0.0.0-20230817173708-d852ddb80c63
	golang.org/x/net v0.25.0
)

require (
	github.com/chainreactors/files v0.0.0-20231102192550-a652458cee26 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/btree v1.0.1 // indirect
	github.com/klauspost/cpuid/v2 v2.2.6 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/riobard/go-bloom v0.0.0-20200614022211-cdc8013cb5b3 // indirect
	github.com/templexxx/cpu v0.1.1 // indirect
	golang.org/x/sys v0.30.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gopkg.in/check.v1 v1.0.0-20180628173108-788fd7840127 // indirect
	gvisor.dev/gvisor v0.0.0-20221203005347-703fd9b7fbc0 // indirect
)

replace github.com/chainreactors/proxyclient => github.com/chainreactors/proxyclient v1.0.3
