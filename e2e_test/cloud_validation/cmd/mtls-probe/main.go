package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hkt999rtk/rtk_cloud_workspace/e2e_test/cloud_validation/mtlsclient"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

var probeBearerGET = mtlsclient.ProbeBearerGET

func run(args []string, stderr io.Writer) int {
	var url, cert, key, ca, tokenFile, expectedRaw string
	var timeout time.Duration
	fs := flag.NewFlagSet("mtls-probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&url, "url", "", "HTTPS probe endpoint")
	fs.StringVar(&cert, "cert", "", "PEM client certificate chain")
	fs.StringVar(&key, "key", "", "PEM client private key")
	fs.StringVar(&ca, "ca", "", "optional PEM server CA")
	fs.StringVar(&tokenFile, "token-file", "", "mode-0600 JSON file containing access_token")
	fs.StringVar(&expectedRaw, "expect-http-status", "200", "comma-separated accepted HTTP statuses")
	fs.DurationVar(&timeout, "timeout", 15*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if url == "" || cert == "" || key == "" || tokenFile == "" {
		fmt.Fprintln(stderr, "url, cert, key, and token-file are required")
		return 2
	}
	status, err := probeBearerGET(context.Background(), url, cert, key, ca, tokenFile, timeout)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !expectedStatus(expectedRaw, status) {
		fmt.Fprintf(stderr, "mTLS probe returned HTTP %d\n", status)
		return 1
	}
	return 0
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
