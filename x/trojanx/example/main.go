package main

import (
	"crypto/tls"
	"github.com/chainreactors/rem/x/trojanx"
	"github.com/chainreactors/rem/x/xtls"
	"github.com/sirupsen/logrus"
	"log"
	"net"
	"net/http"
	"time"
)

func main() {
	go func() {
		server := &http.Server{
			Addr:         "127.0.0.1:80",
			ReadTimeout:  3 * time.Second,
			WriteTimeout: 3 * time.Second,
		}
		server.SetKeepAlivesEnabled(false)
		http.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
			defer request.Body.Close()
			logrus.Debugln(request.RemoteAddr, request.RequestURI)
			host, _, _ := net.SplitHostPort(request.Host)
			switch host {
			default:
				writer.Header().Set("Connection", "close")
				writer.Header().Set("Referrer-Policy", "no-referrer")
				http.Redirect(writer, request, "https://www.baidu.com/", http.StatusFound)
			}
		})
		if err := server.ListenAndServe(); err != nil {
			log.Fatalln(err)
		}
	}()

	signed := xtls.NewRandomTLSKeyPair()
	config := &trojanx.TrojanConfig{
		Password: "password",
		TLSConfig: &trojanx.TLSConfig{
			MinVersion:  tls.VersionTLS13,
			MaxVersion:  tls.VersionTLS13,
			Certificate: *signed,
		},
		ReverseProxyConfig: &trojanx.ReverseProxyConfig{
			Scheme: "http",
			Host:   "127.0.0.1",
			Port:   80,
		},
	}

	srv := trojanx.NewServer(
		trojanx.WithConfig(config),
		trojanx.WithLogger(&logrus.Logger{}),
	)
	if err := srv.ListenAndServe("tcp", ":443"); err != nil {
		logrus.Fatalln(err)
	}
}
