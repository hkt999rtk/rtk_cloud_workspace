package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCreateAccountTransportReopensOriginalOwnerAndPreservesRequest(t *testing.T) {
	const origin = "http://127.0.0.1:18444"
	verified, opened, forwarded, stopped := 0, 0, 0, 0
	transport := environmentCreateAccountTransport{Origin: origin}
	transport.Verify = func(context.Context) error { verified++; return nil }
	transport.Open = func(context.Context) (string, func(), error) {
		opened++
		return "http://127.0.0.1:19999", func() { stopped++ }, nil
	}
	transport.RoundTrip = func(r *http.Request) (*http.Response, error) {
		forwarded++
		if r.URL.Host != "127.0.0.1:19999" || r.URL.Path != "/v1/platform/pki/operations" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer original-user" || r.Header.Get("Idempotency-Key") != "original-operation" {
			t.Fatal("native request changed identity, operation or selected forwarding target")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "original-body" {
			t.Fatal("native request body changed")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}
	for n := 0; n < 2; n++ {
		request, err := http.NewRequest("POST", origin+"/v1/platform/pki/operations", strings.NewReader("original-body"))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer original-user")
		request.Header.Set("Idempotency-Key", "original-operation")
		response, err := transport.RoundTripRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		if request.URL.Host != "127.0.0.1:18444" {
			t.Fatal("original client origin was mutated")
		}
		response.Body.Close()
		response.Body.Close()
	}
	if verified != 4 || opened != 2 || forwarded != 2 || stopped != 2 {
		t.Fatal("selected owner was not independently rechecked before each single request", verified, opened, forwarded, stopped)
	}
}

func TestCreateAccountTransportRejectsDriftWithoutRetryAndCleansCancellation(t *testing.T) {
	for _, mode := range []string{"owner-drift", "owner-drift-after-open", "uncertain-http", "canceled-before-open", "canceled-after-open", "canceled-after-response", "foreign-origin", "foreign-target"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opened, forwarded, verified := 0, 0, 0
			stopped := make(chan struct{}, 2)
			transport := environmentCreateAccountTransport{Origin: "http://127.0.0.1:18444"}
			transport.Verify = func(context.Context) error {
				verified++
				if mode == "owner-drift" || mode == "owner-drift-after-open" && verified == 2 {
					return errors.New("original UID changed")
				}
				return nil
			}
			transport.Open = func(context.Context) (string, func(), error) {
				opened++
				if mode == "canceled-after-open" {
					cancel()
				}
				target := "http://127.0.0.1:19999"
				if mode == "foreign-target" {
					target = "http://198.51.100.1:19999"
				}
				return target, func() { stopped <- struct{}{} }, nil
			}
			transport.RoundTrip = func(*http.Request) (*http.Response, error) {
				forwarded++
				if mode == "uncertain-http" {
					return nil, errors.New("outcome unknown")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
			}
			origin := transport.Origin
			if mode == "foreign-origin" {
				origin = "http://127.0.0.1:18888"
			}
			if mode == "canceled-before-open" {
				cancel()
			}
			request, err := http.NewRequestWithContext(ctx, "POST", origin+"/v1/platform/pki/operations", strings.NewReader("original-body"))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Idempotency-Key", "original-operation")
			response, err := transport.RoundTripRequest(request)
			if mode == "canceled-after-response" {
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				select {
				case <-stopped:
				case <-time.After(time.Second):
					t.Fatal("request cancellation left its private native tunnel running")
				}
				response.Body.Close()
				if len(stopped) != 0 {
					t.Fatal("native tunnel cleaned more than once")
				}
			} else {
				if err == nil || response != nil {
					t.Fatal("unqualified native request completed")
				}
				if opened > 0 && len(stopped) != 1 {
					t.Fatal("failed native request did not clean its exact tunnel")
				}
			}
			if forwarded > 1 {
				t.Fatal("uncertain native request was replayed")
			}
			if mode != "uncertain-http" && mode != "canceled-after-response" && forwarded != 0 {
				t.Fatal("native request sent after owner/origin/cancellation fence failed")
			}
		})
	}
}
