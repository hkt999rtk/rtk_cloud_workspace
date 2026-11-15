package main

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCustodyJobHandle struct {
	invoke  func(context.Context, []string, []byte) ([]byte, error)
	cleanup func(context.Context) error
}

// One operation owns the helpers; only recipes and verified handles are reused.
// Every RPC still executes natively and checks the original Job and Pod twice.
type environmentCustodyJobPool struct {
	mu     sync.Mutex
	parent context.Context
	jobs   map[string]*environmentCustodyJobHandle
	closed bool
}

func (p *environmentCustodyJobPool) run(ctx context.Context, b environmentKubernetesBackend, job deploymentplan.Object, namespace string, args []string, input []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(args) != 4 || (args[0] != "inspect-public-crl" && args[0] != "apply-public-crl") {
		return nil, environmentCreateBootstrapBlocker("active original public custody operation is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.parent == nil {
		p.parent = ctx
	}
	if err := p.parent.Err(); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.parent, cancel)
	defer stop()
	defer cancel()
	if deadline, ok := p.parent.Deadline(); ok {
		var end context.CancelFunc
		callCtx, end = context.WithDeadline(callCtx, deadline)
		defer end()
	}
	key := deploymentplan.Digest(deploymentplan.Object{"environment": b.Store.Environment, "store": b.Store.Root, "kubeconfig_sha256": environmentPreflightBytesSHA256(b.KubeconfigContent), "namespace": namespace, "job": job})
	if helper := p.jobs[key]; helper != nil {
		bounded, end := context.WithTimeout(callCtx, b.RolloutTimeout)
		defer end()
		return helper.invoke(bounded, args, input)
	}
	job = deploymentplan.Clone(job)
	if deadline, ok := callCtx.Deadline(); ok {
		seconds := math.Ceil(time.Until(deadline).Seconds())
		if seconds < 1 {
			return nil, context.DeadlineExceeded
		}
		deploymentplan.Map(job["spec"])["activeDeadlineSeconds"] = seconds
	}
	b.custodyJobs = nil
	return runEnvironmentCustodyJobOnce(callCtx, b, job, namespace, args, input, func(helper *environmentCustodyJobHandle) {
		if p.jobs == nil {
			p.jobs = map[string]*environmentCustodyJobHandle{}
		}
		p.jobs[key] = helper
	})
}

func (p *environmentCustodyJobPool) Close(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	jobs := p.jobs
	p.jobs = nil
	p.mu.Unlock()
	// Helpers are independent UID-bound resources. Keep cleanup bounded without
	// serializing every controller deletion into the operation's tail latency.
	limit := make(chan struct{}, 4)
	results := make(chan error, len(jobs))
	for _, helper := range jobs {
		go func() {
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
				results <- helper.cleanup(ctx)
			case <-ctx.Done():
				results <- ctx.Err()
			}
		}()
	}
	var result error
	for range jobs {
		result = errors.Join(result, <-results)
	}
	return result
}
