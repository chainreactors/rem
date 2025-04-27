package cmd

import (
	"bufio"
	"fmt"
	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/runner"
	"github.com/chainreactors/rem/x/utils"
	"github.com/jessevdk/go-flags"
	"github.com/kballard/go-shellquote"
	"os"
	"time"
)

var ver = ""

func RUN() {
	defer exit()
	var option runner.Options
	parser := flags.NewParser(&option, flags.Default)
	parser.Usage = `
	WIKI: https://chainreactors.github.io/wiki/rem

	QUICKSTART:
		serving:
			./rem

		reverse socks5 proxy:
			./rem -c [link]

		serve socks5 proxy on client:
			./rem -c [link] -m proxy

		remote port forward:
			./rem -c [link] -l port://:8080

		local port forward:
			./rem -c [link] -r port://:8080
		
`
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

	_, err := parser.ParseArgs(args)
	if err != nil {
		if err.(*flags.Error).Type != flags.ErrHelp {
			fmt.Println(err.Error())
		}
		return
	}

	if option.Version {
		fmt.Println(ver)
		return
	}
	// 如果命令行参数为空，使用编译时设置的默认值
	if option.Mod == "" {
		option.Mod = runner.DefaultMod
	}
	if len(option.ConsoleAddr) == 0 {
		option.ConsoleAddr = []string{runner.DefaultConsole}
	}
	if option.LocalAddr == "" {
		option.LocalAddr = runner.DefaultLocal
	}
	if option.RemoteAddr == "" {
		option.RemoteAddr = runner.DefaultRemote
	}

	if option.Debug {
		utils.Log = logs.NewLogger(logs.DebugLevel)
		utils.Log.LogFileName = "maitai.log"
		utils.Log.Init()
	} else if option.Detail {
		utils.Log = logs.NewLogger(utils.IOLog)
	} else if option.Quiet {
		utils.Log = logs.NewLogger(100)
	} else {
		utils.Log = logs.NewLogger(logs.InfoLevel)
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
