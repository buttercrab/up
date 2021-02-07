package main

import (
	"github.com/vbauerster/mpb/v5"
	"github.com/vbauerster/mpb/v5/decor"
	"io"
)

func MakeDecorator() func(filler mpb.BarFiller) mpb.BarFiller {
	return func(base mpb.BarFiller) mpb.BarFiller {
		return mpb.BarFillerFunc(func(w io.Writer, repWidth int, stat decor.Statistics) {
			select {}
		})
	}
}
