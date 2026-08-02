package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hkt999rtk/rtk_cloud_workspace/e2e_test/cloud_validation/mtlsclient"
)

func main() {
	var url, cert, key, ca, tokenFile, expectedRaw string
	var timeout time.Duration
	flag.StringVar(&url, "url", "", "HTTPS probe endpoint")
	flag.StringVar(&cert, "cert", "", "PEM client certificate chain")
	flag.StringVar(&key, "key", "", "PEM client private key")
	flag.StringVar(&ca, "ca", "", "optional PEM server CA")
	flag.StringVar(&tokenFile, "token-file", "", "mode-0600 JSON file containing access_token")
	flag.StringVar(&expectedRaw, "expect-http-status", "200", "comma-separated accepted HTTP statuses")
	flag.DurationVar(&timeout, "timeout", 15*time.Second, "request timeout")
	flag.Parse()
	if url == "" || cert == "" || key == "" || tokenFile == "" {
		fmt.Fprintln(os.Stderr, "url, cert, key, and token-file are required")
		os.Exit(2)
	}
	status, err := mtlsclient.ProbeBearerGET(context.Background(), url, cert, key, ca, tokenFile, timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !expectedStatus(expectedRaw, status) {
		fmt.Fprintf(os.Stderr, "mTLS probe returned HTTP %d\n", status)
		os.Exit(1)
	}
}

func expectedStatus(raw string, actual int) bool {
	for _, item := range strings.Split(raw, ",") {
		value, err := strconv.Atoi(strings.TrimSpace(item))
		if err == nil && value == actual {
			return true
		}
	}
	return false
}
