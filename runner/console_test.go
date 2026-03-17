package runner

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/x/utils"
)

func TestNewConsole(t *testing.T) {
	u, _ := url.Parse("rem+tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw")
	proxy, err := proxyclient.NewClient(u)
	if err != nil {
		panic(err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: proxy,
		},
	}
	_, err = client.Get("http://localhost:8000")
	if err != nil {
		panic(err)
	}

	time.Sleep(time.Second)
}

func TestClient(t *testing.T) {
	utils.Log.SetLevel(logs.DebugLevel)
	var copt Options
	// -c tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw -n
	copt.ParseArgs([]string{"-c", "tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw", "-n", "-a", "test", "--debug"})
	client, err := copt.Prepare()
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	err = client.Run()
	if err != nil {
		fmt.Println(err.Error())
		return
	}
}

func TestRemProxy(t *testing.T) {
	remurl, _ := url.Parse("tcp://nonenonenonenone:@127.0.0.1:1234/?wrapper=raw")
	client, err := newRemProxyClient(remurl, nil)
	if err != nil {
		return
	}
	dial, err := client(nil, "tcp", "127.0.0.1:1111")
	if err != nil {
		return
	}
	dial.Write([]byte("test"))
}

func TestRemHTTPProxy(t *testing.T) {
	remurl, _ := url.Parse("tcp://nonenonenonenone:@127.0.0.1:1234/?wrapper=raw")
	pc, err := newRemProxyClient(remurl, nil)
	if err != nil {
		return
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: pc,
		},
	}
	_, err = client.Get("http://127.0.0.1:1111/")
	if err != nil {
		panic(err)
	}
}

func TestServer(t *testing.T) {
	utils.Log.SetLevel(logs.DebugLevel)
	var server *Console
	go func() {
		var err error
		server, err = NewConsoleWithCMD(" --debug -s tcp://0.0.0.0:0/?wrapper=raw -i 127.0.0.1")
		err = server.Run()
		if err != nil {
			fmt.Println(err.Error())
			return
		}
	}()
	time.Sleep(10 * time.Second)

	_, err := server.Fork("test", []string{"-m", "reverse", "-l", "127.0.0.1:8000", "-r", "port://:12345"})
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	select {}
}
