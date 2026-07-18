package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/hkt999rtk/rtk_cloud_workspace/e2e_test/cloud_validation/mtlsclient"
)

func main() {
	var url, cert, key, ca, request, output string
	var timeout time.Duration
	flag.StringVar(&url, "url", "", "HTTPS token endpoint")
	flag.StringVar(&cert, "cert", "", "PEM client certificate chain")
	flag.StringVar(&key, "key", "", "PEM client private key")
	flag.StringVar(&ca, "ca", "", "optional PEM server CA")
	flag.StringVar(&request, "request", "", "JSON request file")
	flag.StringVar(&output, "output", "", "mode-0600 response file")
	flag.DurationVar(&timeout, "timeout", 15*time.Second, "request timeout")
	flag.Parse()
	if url == "" || cert == "" || key == "" || request == "" || output == "" {
		fmt.Fprintln(os.Stderr, "url, cert, key, request, and output are required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := mtlsclient.Request(ctx, url, cert, key, ca, request, output, timeout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
