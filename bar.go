package main

import (
	"fmt"
	"github.com/vbauerster/mpb/v5"
	"github.com/vbauerster/mpb/v5/decor"
	"path"
)

func MakeBar(p *mpb.Progress, task UpdateAble, opt *Options) *mpb.Bar {
	taskName := fmt.Sprintf("%s:", task.Name())
	var b *mpb.Bar

	if task.HasPercentage() {
		total := task.Init(path.Join(opt.rootPath, task.Name()))
		b = p.AddBar(
			total,
			mpb.BarFillerMiddleware(MakeDecorator()),
			mpb.PrependDecorators(decor.Name(taskName, decor.WCSyncSpaceR)),
		)
	} else {
		total := task.Init(path.Join(opt.rootPath, task.Name()))
		b = p.AddSpinner(
			total,
			mpb.SpinnerOnLeft,
			mpb.BarFillerMiddleware(MakeDecorator()),
			mpb.SpinnerStyle([]string{"⡇", "⣆", "⣤", "⣰", "⢸", "⠹", "⠛", "⠏"}),
			mpb.PrependDecorators(decor.Name(taskName, decor.WCSyncSpaceR)),
		)
	}

	return b
}
