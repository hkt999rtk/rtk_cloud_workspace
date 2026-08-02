package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hkt999rtk/rtk_cloud_workspace/e2e_test/cloud_validation/mtlsclient"
)

func main() {
	var url, cert, key, ca, request, output, expectedHTTPStatuses string
	var timeout time.Duration
	flag.StringVar(&url, "url", "", "HTTPS token endpoint")
	flag.StringVar(&cert, "cert", "", "PEM client certificate chain")
	flag.StringVar(&key, "key", "", "PEM client private key")
	flag.StringVar(&ca, "ca", "", "optional PEM server CA")
	flag.StringVar(&request, "request", "", "JSON request file")
	flag.StringVar(&output, "output", "", "mode-0600 response file")
	flag.StringVar(&expectedHTTPStatuses, "expect-http-status", "", "comma-separated non-success HTTP statuses accepted without writing a response")
	flag.DurationVar(&timeout, "timeout", 15*time.Second, "request timeout")
	flag.Parse()
	if url == "" || cert == "" || key == "" || request == "" || output == "" {
		fmt.Fprintln(os.Stderr, "url, cert, key, request, and output are required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := mtlsclient.Request(ctx, url, cert, key, ca, request, output, timeout); err != nil {
		var statusErr *mtlsclient.HTTPStatusError
		if errors.As(err, &statusErr) && expectedStatus(expectedHTTPStatuses, statusErr.StatusCode) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
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
