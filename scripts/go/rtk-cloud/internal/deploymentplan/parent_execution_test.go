package deploymentplan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type parentExecutionFixture struct {
	memoryBackend
	root                string
	checkErr, recordErr error
	checks, records     int
	beforeWrite         func()
}

func (b *parentExecutionFixture) CheckParentExecution(_ context.Context, p *Plan, root string) error {
	b.checks++
	if b.checkErr != nil {
		return b.checkErr
	}
	if root != b.root || p.Environment == "" {
		return errors.New("wrong parent")
	}
	raw, err := os.ReadFile(filepath.Join(root, "deployment", ".execution-active", "parent"))
	if err != nil || string(raw) != "original-parent" {
		return errors.New("parent owner changed")
	}
	return nil
}
func (b *parentExecutionFixture) RecordParentExecutionLease(_ context.Context, p *Plan, owner LeaseOwner) error {
	b.records++
	if owner.ResourceID != "Lease/"+p.Stack+"-video-cloud/rtk-deployment-execution" || owner.UID == "" || owner.Holder == "" {
		return errors.New("no real lease")
	}
	return b.recordErr
}
func (b *parentExecutionFixture) Write(ctx context.Context, action string, body Object, dry bool) (Object, error) {
	if !dry && b.beforeWrite != nil {
		b.beforeWrite()
	}
	return b.memoryBackend.Write(ctx, action, body, dry)
}

func TestChildPlanRetainsParentFenceAndUsesRealLease(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"complete", "invalid-parent", "unrecorded-lease", "failed-admission", "failed-acceptance"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				profile := phaseObservationProfile(t, environment, false)
				active := filepath.Join(profile.root, "deployment", ".execution-active")
				if err := os.MkdirAll(active, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(active, "parent"), []byte("original-parent"), 0600); err != nil {
					t.Fatal(err)
				}
				b := &parentExecutionFixture{memoryBackend: profile.backend.memoryBackend, root: profile.root}
				if mode == "invalid-parent" {
					b.checkErr = errors.New("private parent failure")
				}
				if mode == "unrecorded-lease" {
					b.recordErr = errors.New("journal unavailable")
				}
				if mode == "failed-admission" {
					b.failID = profile.first
				}
				qualify := func(context.Context, *Plan) error { return nil }
				fence := func(ctx context.Context, p *Plan) error { return b.CheckParentExecution(ctx, p, profile.root) }
				accept := func(context.Context, *Plan) error {
					if mode == "failed-acceptance" {
						return errors.New("required public endpoint failed")
					}
					return nil
				}
				err := profile.plan.Execute(context.Background(), b, profile.plan.SHA256(), profile.root, qualify, fence, accept)
				if (err == nil) != (mode == "complete") {
					t.Fatalf("mode %s: %v", mode, err)
				}
				if b.leaseHeld {
					t.Fatal("child did not release its exact native Lease")
				}
				if raw, err := os.ReadFile(filepath.Join(active, "parent")); err != nil || string(raw) != "original-parent" {
					t.Fatal("child replaced or archived parent ownership")
				}
				if mode == "invalid-parent" || mode == "unrecorded-lease" || mode == "failed-admission" {
					if len(b.written) != 0 {
						t.Fatal("unverified ownership/admission permitted persisted child writes")
					}
				}
				if mode == "complete" {
					if b.records != 1 || b.checks < 2 || !readExecutionJournal(t, profile.root, profile.plan.SHA256()).Accepted {
						t.Fatal("child skipped genuine Lease binding or accepted journal")
					}
				}
			})
		}
	}
}
