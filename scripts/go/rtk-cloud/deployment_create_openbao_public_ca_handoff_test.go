package main

import (
	"encoding/pem"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func TestOpenBaoPublicCAOverlapPreservesOriginalAndIsIdempotent(t *testing.T) {
	cfg, p, runtime, service := createServiceRecipeFixture(t, "dev")
	_ = cfg
	_ = p
	_ = runtime
	original := service.Root.Certificate
	block, _ := pem.Decode([]byte(original))
	if block == nil {
		t.Fatal("root fixture")
	}
	managed := strings.Replace(original, "CERTIFICATE", "CERTIFICATE", 1)
	// Generate a second independent public root using the real isolated fixture.
	cfg2, p2, runtime2, service2 := createServiceRecipeFixture(t, "staging")
	_ = cfg2
	_ = p2
	_ = runtime2
	managed = service2.Root.Certificate
	root, _ := pem.Decode([]byte(managed))
	if root == nil {
		t.Fatal("managed fixture")
	}
	pin := environmentPreflightBytesSHA256(root.Bytes)
	now := time.Now().UTC()
	bundle, err := openBaoCAOverlap(original, managed, original, pin, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(bundle, strings.TrimSpace(original)) || !strings.Contains(bundle, strings.TrimSpace(managed)) {
		t.Fatal("missing original or managed CA")
	}
	again, err := openBaoCAOverlap(original, managed, bundle, pin, now)
	if err != nil || bundle != again {
		t.Fatal("retry changed bundle", err)
	}
	obj := deploymentplan.Object{"kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "ca", "namespace": "video-cloud-dev-video-cloud"}, "data": deploymentplan.Object{"ca.crt": original, "unchanged": "preserve"}}
	before := deploymentplan.Digest(obj)
	d, err := openBaoCAOverlapDefinition(obj, "original-uid", managed, pin, now)
	if err != nil || deploymentplan.Map(d.Object["data"])["unchanged"] != "preserve" || deploymentplan.Digest(obj) != before {
		t.Fatal("source changed", err)
	}
	if _, err := openBaoCAOverlap(original, managed, "foreign", pin, now); err == nil {
		t.Fatal("foreign current CA accepted")
	}
	if _, err := openBaoCAOverlap(original, managed, original, strings.Repeat("f", 64), now); err == nil {
		t.Fatal("foreign managed root accepted")
	}
}

func TestOpenBaoCAHandoffUsesOnlyCompletedSameOwnerSuccessor(t *testing.T) {
	source := deploymentplan.Object{"kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "ca", "namespace": "video-cloud-dev-video-cloud"}, "data": deploymentplan.Object{"ca.crt": "original"}}
	successor := deploymentplan.Clone(source)
	deploymentplan.Map(successor["data"])["ca.crt"] = "recorded successor"
	write := activeTransportRenewalResource{ID: deploymentplan.ID(source), UID: "same-owner", Before: deploymentplan.Clone(source), Desired: successor}
	got, err := advanceOpenBaoCASource(source, "same-owner", []activeTransportRenewalResource{write})
	if err != nil || deploymentplan.Map(got["data"])["ca.crt"] != "recorded successor" {
		t.Fatal("recorded successor rejected", err)
	}
	write.UID = "foreign"
	if _, err = advanceOpenBaoCASource(source, "same-owner", []activeTransportRenewalResource{write}); err == nil {
		t.Fatal("foreign owner accepted")
	}
	write.UID = "same-owner"
	deploymentplan.Map(write.Before["data"])["ca.crt"] = "other"
	if _, err = advanceOpenBaoCASource(source, "same-owner", []activeTransportRenewalResource{write}); err == nil {
		t.Fatal("unrelated predecessor accepted")
	}
}
