package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Each authenticated request forwards once to the original Deployment owner.
// Account Pod rollouts do not invalidate this client/session, and an uncertain
// HTTP outcome is never retried. The virtual origin is fixed and loopback-only;
// the transport binds each real tunnel to the original namespace/controller.
type environmentCreateAccountTransport struct {
	Origin    string
	Verify    func(context.Context) error
	Open      func(context.Context) (string, func(), error)
	RoundTrip func(*http.Request) (*http.Response, error)
}

type environmentCreateAccountResponseBody struct {
	io.ReadCloser
	finish func()
}

func (body environmentCreateAccountResponseBody) Close() error {
	err := body.ReadCloser.Close()
	body.finish()
	return err
}

func (transport environmentCreateAccountTransport) RoundTripRequest(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.URL.Scheme+"://"+request.URL.Host != transport.Origin || transport.Verify == nil || transport.Open == nil {
		return nil, environmentCreateBootstrapBlocker("native Account Manager request escaped its original selected transport")
	}
	ctx := request.Context()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := transport.Verify(ctx); err != nil {
		return nil, err
	}
	origin, stop, err := transport.Open(ctx)
	if err != nil {
		return nil, err
	}
	if stop == nil {
		return nil, environmentCreateBootstrapBlocker("native Account Manager tunnel has no original cleanup owner")
	}
	var once sync.Once
	cleanup := func() { once.Do(stop) }
	stopCancellation := context.AfterFunc(ctx, cleanup)
	finish := func() { stopCancellation(); cleanup() }
	target, err := url.Parse(origin)
	if err != nil || target.Scheme != "http" || target.User != nil || target.Path != "" || target.RawQuery != "" || target.Fragment != "" || net.ParseIP(target.Hostname()) == nil || !net.ParseIP(target.Hostname()).IsLoopback() {
		finish()
		return nil, environmentCreateBootstrapBlocker("native Account Manager tunnel is not its own exact loopback forwarding target")
	}
	if err := transport.Verify(ctx); err != nil {
		finish()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		finish()
		return nil, err
	}
	forward := request.Clone(ctx)
	u := *request.URL
	u.Scheme, u.Host = target.Scheme, target.Host
	forward.URL, forward.Host = &u, ""
	roundTrip := transport.RoundTrip
	if roundTrip == nil {
		// A fresh connection per request prevents net/http from replaying an
		// idempotency-key mutation after a reused connection's uncertain error.
		actual := http.DefaultTransport.(*http.Transport).Clone()
		actual.Proxy, actual.DisableKeepAlives = nil, true
		roundTrip = actual.RoundTrip
	}
	response, err := roundTrip(forward)
	if err != nil {
		finish()
		return nil, err
	}
	if response == nil || response.Body == nil {
		finish()
		return nil, environmentCreateBootstrapBlocker("native Account Manager response is unavailable")
	}
	response.Body = environmentCreateAccountResponseBody{response.Body, finish}
	return response, nil
}

type environmentCreateAccountRoundTripper struct {
	transport environmentCreateAccountTransport
}

func (adapter environmentCreateAccountRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return adapter.transport.RoundTripRequest(request)
}

func newEnvironmentCreateAccountClient(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, execution *environmentCreateExecution) (string, *http.Client, error) {
	if execution == nil || backend.Store.Root != execution.Store.Root || backend.Store.Environment != cfg.Environment {
		return "", nil, environmentCreateBootstrapBlocker("native Account Manager client requires its original selected execution and cluster")
	}
	namespace := cfg.Values["CLOUD_STACK_NAME"] + "-account-manager"
	read := func(ctx context.Context, id string) (string, error) {
		actual, err := backend.Read(ctx, id)
		if err != nil || actual == nil || deploymentplan.ID(actual) != id || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) == "" || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil {
			return "", environmentCreateBootstrapBlocker("original selected Account Manager native owner cannot be read")
		}
		return deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]), nil
	}
	namespaceID, deploymentID := "Namespace//"+namespace, "Deployment/"+namespace+"/account-manager"
	namespaceUID, err := read(ctx, namespaceID)
	if err != nil {
		return "", nil, err
	}
	deploymentUID, err := read(ctx, deploymentID)
	if err != nil {
		return "", nil, err
	}
	const origin = "http://127.0.0.1:18444"
	transport := environmentCreateAccountTransport{Origin: origin}
	transport.Verify = func(ctx context.Context) error {
		if err := execution.Check(ctx); err != nil {
			return err
		}
		ns, nsErr := read(ctx, namespaceID)
		deployment, deploymentErr := read(ctx, deploymentID)
		if nsErr != nil || deploymentErr != nil || ns != namespaceUID || deployment != deploymentUID {
			return environmentCreateBootstrapBlocker("original Account Manager namespace or Deployment changed before native request; replacement identity is never adopted")
		}
		return nil
	}
	transport.Open = func(ctx context.Context) (string, func(), error) {
		return openEnvironmentCreateTunnel(ctx, backend, namespace, "deployment/account-manager", 8080)
	}
	return origin, &http.Client{Timeout: 30 * time.Second, Transport: environmentCreateAccountRoundTripper{transport}}, nil
}
