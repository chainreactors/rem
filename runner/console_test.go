package runner

import (
	"fmt"
	"github.com/chainreactors/logs"
	"github.com/chainreactors/proxyclient"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestNewConsole(t *testing.T) {
	u, _ := url.Parse("rem+tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw")
	proxy, err := proxyclient.NewClient(u)

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: proxy.DialContext,
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

func TestServer(t *testing.T) {
	utils.Log.SetLevel(logs.DebugLevel)
	var server *Console
	go func() {
		var err error
		server, err = NewConsoleWithCMD(" --debug -c tcp:///?wrapper=raw -i 127.0.0.1")
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
