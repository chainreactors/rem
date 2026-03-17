package agent

import (
	"context"
	"net"
	"testing"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	if utils.Log == nil {
		utils.Log = logs.NewLogger(logs.WarnLevel)
	}
}

func BenchmarkBridgeClose(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ctrlLocal, ctrlPeer := net.Pipe()
		drainDone := make(chan struct{})
		go func() {
			defer close(drainDone)
			for {
				if _, err := cio.ReadMsg(ctrlPeer); err != nil {
					return
				}
			}
		}()

		streamLocal, streamPeer := net.Pipe()
		remoteLocal, remotePeer := net.Pipe()

		ag := &Agent{
			stream0: ctrlLocal,
			ctx:     context.Background(),
			log:     utils.NewRingLogWriter(8),
		}
		br := &Bridge{
			id:     uint64(i + 1),
			stream: streamLocal,
			remote: remoteLocal,
			cancel: func() {},
			agent:  ag,
		}

		if err := br.Close(); err != nil {
			b.Fatal(err)
		}

		_ = streamPeer.Close()
		_ = remotePeer.Close()
		_ = ctrlLocal.Close()
		_ = ctrlPeer.Close()
		<-drainDone
	}
}
