package main

import (
	"os"

	"rtk-cloud-workspace/loadtests/home-100k/internal/home100k"
)

func main() {
	os.Exit(home100k.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
