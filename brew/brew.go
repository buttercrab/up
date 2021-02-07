package brew

import (
	"os/exec"
)

type Brew struct {
}

func NewBrew() Brew {
	return Brew{}
}

func (b Brew) Name() string {
	return "brew"
}

func (b Brew) Pre() []string {
	return []string{}
}

func (b Brew) HasPercentage() bool {
	return false
}

func (b Brew) Init(_ string) int64 {
	return 1
}

func (b Brew) Update(progress chan int64, err chan error) {
	cmd := exec.Command("brew", "upgrade")
	if e := cmd.Start(); e != nil {
		err <- e
		return
	}
	if e := cmd.Wait(); e != nil {
		err <- e
		return
	}
	progress <- 1
}
