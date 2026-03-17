package main

import (
	//_ "net/http/pprof"

	"github.com/chainreactors/rem/cmd/cmd"
)

func main() {
	//go func() {
	//	http.ListenAndServe("localhost:6060", nil)
	//}()

	cmd.RUN()
}
