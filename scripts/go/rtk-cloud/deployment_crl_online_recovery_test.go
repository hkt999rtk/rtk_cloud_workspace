package main

import (
	"strings"
	"testing"
	"time"
)

func TestCRLMaintenanceOnlineDueUsesOriginalAuthority(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		t.Run(env, func(t *testing.T) {
			item, _, now := maintenanceFixture(t, env, time.Now().UTC().Truncate(time.Second).Add(-time.Hour))
			item.Issuer.Document["signer_provider"] = "openbao"
			item.Issuer.Document["certificate_fingerprint_sha256"] = item.Issuer.RootFingerprint
			item.Issuer.Reference = "pki-issuers/device/" + item.Issuer.ID + "/v1"
			args, due, err := maintenanceOnlineArguments(env, item.Issuer, item.Snapshot.Prior, now)
			if err != nil || !due || len(args) != 6 || args[1] != env || args[3] != item.Issuer.RootFingerprint || args[4] != item.Issuer.Reference || args[5] != item.Snapshot.Prior.Digest {
				t.Fatalf("original expired authority not bound: %v %v", args, err)
			}
			if _, _, err := maintenanceOnlineArguments("other", item.Issuer, item.Snapshot.Prior, now); err == nil {
				t.Fatal("cross-environment online recovery accepted")
			}
			fresh, key, _ := maintenanceFixture(t, env, now.Add(48*time.Hour))
			_ = key
			fresh.Issuer.Document["signer_provider"] = "openbao"
			if args, due, err := maintenanceOnlineArguments(env, fresh.Issuer, fresh.Snapshot.Prior, now); err != nil || due || len(args) != 0 {
				t.Fatalf("healthy native worker unnecessarily invoked: %v %v", args, err)
			}
			item.Snapshot.Prior.Digest = strings.Repeat("0", 64)
			if _, _, err := maintenanceOnlineArguments(env, item.Issuer, item.Snapshot.Prior, now); err == nil {
				t.Fatal("damaged prior accepted")
			}
		})
	}
}
