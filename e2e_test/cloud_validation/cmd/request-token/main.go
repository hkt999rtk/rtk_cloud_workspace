package main

import (
	"context"
	"errors"
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

var requestToken = mtlsclient.Request

func run(args []string, stderr io.Writer) int {
	var url, cert, key, ca, request, output, expectedHTTPStatuses string
	var timeout time.Duration
	fs := flag.NewFlagSet("request-token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&url, "url", "", "HTTPS token endpoint")
	fs.StringVar(&cert, "cert", "", "PEM client certificate chain")
	fs.StringVar(&key, "key", "", "PEM client private key")
	fs.StringVar(&ca, "ca", "", "optional PEM server CA")
	fs.StringVar(&request, "request", "", "JSON request file")
	fs.StringVar(&output, "output", "", "mode-0600 response file")
	fs.StringVar(&expectedHTTPStatuses, "expect-http-status", "", "comma-separated non-success HTTP statuses accepted without writing a response")
	fs.DurationVar(&timeout, "timeout", 15*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if url == "" || cert == "" || key == "" || request == "" || output == "" {
		fmt.Fprintln(stderr, "url, cert, key, request, and output are required")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := requestToken(ctx, url, cert, key, ca, request, output, timeout); err != nil {
		var statusErr *mtlsclient.HTTPStatusError
		if errors.As(err, &statusErr) && expectedStatus(expectedHTTPStatuses, statusErr.StatusCode) {
			return 0
		}
		fmt.Fprintln(stderr, err)
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
