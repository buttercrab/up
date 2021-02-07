package main

import (
	"github.com/buttercrab/up/brew"
	flag "github.com/integrii/flaggy"
	"github.com/vbauerster/mpb/v5"
	"log"
	"runtime"
	"sync"
)

type Options struct {
	rootPath string
}

func getOption() *Options {
	flag.SetName("up")
	flag.SetDescription("upgrade packages")
	flag.SetVersion("0.1")

	var opt Options

	opt.rootPath = "~/.up"
	flag.String(&opt.rootPath, "", "root", "root of up configure dir (defaults to `~/.up`)")
	flag.Parse()

	return &opt
}

func run(opt *Options) {
	updates := []UpdateAble{brew.NewBrew()}

	doneWg := new(sync.WaitGroup)
	p := mpb.New(mpb.WithWaitGroup(doneWg))

	for _, v := range updates {
		v := v
		go func() {
			b := MakeBar(p, v, opt)

			progress := make(chan int64)
			err := make(chan error)

			go v.Update(progress, err)

			for {
				select {
				case x := <-progress:
					b.SetCurrent(x)
					break
				case <-err:
					break
				default:
				}
			}
		}()
	}

	p.Wait()
}

func main() {
	if runtime.GOOS == "windows" {
		log.Fatal("windows not supported")
	} else {
		run(getOption())
	}
}
