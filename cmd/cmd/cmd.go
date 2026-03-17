package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/runner"
	"github.com/chainreactors/rem/x/utils"
	"github.com/kballard/go-shellquote"
	"os"
	"time"
)

var ver = ""

const usage = `
	WIKI: https://chainreactors.github.io/wiki/rem

	QUICKSTART:
		start server (listen on default address):
			./rem -s tcp://0.0.0.0:34996

		or just (uses default server address):
			./rem

		reverse socks5 proxy (client connects to server):
			./rem -c [link]

		serve socks5 proxy on client:
			./rem -c [link] -m proxy

		remote port forward:
			./rem -c [link] -l port://:8080

		local port forward:
			./rem -c [link] -r port://:8080

`

func RUN() {
	defer exit()
	var option runner.Options
	var args []string
	reader := bufio.NewReader(os.Stdin)
	ch := make(chan string)

	go func() {
		data, _ := reader.ReadString('\n')
		ch <- data
	}()

	select {
	case input := <-ch:
		split, err := shellquote.Split(input)
		if err != nil {
			logs.Log.Error(err)
			return
		}
		args = split
	case <-time.After(100 * time.Millisecond):
	}

	if len(args) == 0 {
		args = os.Args[1:]
	}

	err := option.ParseArgs(args)
	if err != nil {
		if errors.Is(err, runner.ErrHelpRequested) {
			fmt.Print(usage)
			fmt.Print(runner.OptionsUsage())
		} else {
			fmt.Println(err.Error())
		}
		return
	}

	if option.Version {
		fmt.Println(ver)
		return
	}

	if option.List {
		runner.ListRegistered()
		return
	}

	runner, err := option.Prepare()
	if err != nil {
		fmt.Println(err.Error())
		return
	}

	utils.Log.Debugf("rem version: %s", ver)
	err = runner.Run()
	if err != nil {
		fmt.Println(err.Error())
		return
	}
}

func exit() {
	os.Exit(0)
}
