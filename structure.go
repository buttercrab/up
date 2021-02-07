package main

type UpdateAble interface {
	Name() string
	Pre() []string
	HasPercentage() bool
	Init(path string) int64
	Update(progress chan int64, err chan error)
}
