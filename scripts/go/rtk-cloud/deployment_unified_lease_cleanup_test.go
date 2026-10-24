package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type failedAcquisitionAPI struct {
	executionLeaseAPIFixture
	mode    string
	created bool
	cancel  context.CancelFunc
	t       *testing.T
}

func (f *failedAcquisitionAPI) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	if f.created {
		deadline, ok := ctx.Deadline()
		if ctx.Err() != nil || !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
			f.t.Fatal("failed acquisition cleanup reused cancellation or has no bounded deadline")
		}
		if f.mode == "read-failure" {
			return nil, errors.New("private-api-output")
		}
	}
	return f.executionLeaseAPIFixture.Read(ctx, id)
}

func (f *failedAcquisitionAPI) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	actual, err := f.executionLeaseAPIFixture.Write(ctx, action, body, dry)
	if dry || err != nil {
		return actual, err
	}
	f.created = true
	switch f.mode {
	case "cancelled":
		f.cancel()
		return nil, context.Canceled
	case "malformed-response":
		deploymentplan.Map(actual["spec"])["holderIdentity"] = "unexpected-response-owner"
		return actual, nil
	case "concurrent-owner":
		deploymentplan.Map(f.object["spec"])["holderIdentity"] = "another-controller"
	case "replaced-uid":
		deploymentplan.Map(f.object["metadata"])["uid"] = "replacement-lease"
		return actual, errors.New("private-api-output")
	case "delete-race":
		f.replaceOnDelete = true
	}
	return nil, errors.New("private-api-output")
}

func (f *failedAcquisitionAPI) DeleteExecutionLease(ctx context.Context, id, uid, version string) error {
	if f.mode == "delete-failure" {
		return errors.New("private-api-output")
	}
	return f.executionLeaseAPIFixture.DeleteExecutionLease(ctx, id, uid, version)
}

func TestFailedLeaseAcquisitionReleasesOnlyItsConfirmedOriginalOwner(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"lost-response", "cancelled", "malformed-response", "concurrent-owner", "replaced-uid", "read-failure", "delete-failure", "delete-race"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment}}
				api := &failedAcquisitionAPI{mode: mode, cancel: cancel, t: t}
				lease, err := acquireEnvironmentExecutionLease(ctx, api, plan)
				if err == nil || lease != nil || api.writes != 1 || strings.Contains(err.Error(), "private-api-output") {
					t.Fatal("failed acquisition was accepted, retried or exposed private diagnostics", err)
				}
				cleaned := mode == "lost-response" || mode == "cancelled" || mode == "malformed-response"
				if cleaned {
					if api.object != nil || api.deletes != 1 {
						t.Fatal("failed acquisition retained its original lease")
					}
				} else if api.object == nil || api.deletes != 0 {
					t.Fatal("failed acquisition cleanup deleted an unverified or concurrent owner")
				}
				if !cleaned && mode != "concurrent-owner" {
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.execution.cleanup" || blocker.Resource != "Lease/"+plan.Stack+"-video-cloud/rtk-deployment-execution" {
						t.Fatal("cleanup failure lacks its safe resource and error code", err)
					}
				}
			})
		}
	}
}
