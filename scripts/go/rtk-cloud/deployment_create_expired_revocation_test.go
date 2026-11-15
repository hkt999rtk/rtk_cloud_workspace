package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestExpiredBootstrapRevocationCompatibilityPreservesOriginalConfiguration(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"complete", "lost-response", "foreign-scope", "not-expired", "unrelated-change"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				_, _, _, s := createServiceRecipeFixture(t, environment)
				s.Intermediate.Reference = "pki-issuers/service/" + s.Intermediate.ID + "/v1"
				cfg, _, operator, artifact := createStateFixture(t, environment)
				e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = e.Close() })
				at := time.Now().UTC()
				expires := at.Add(-time.Minute)
				if mode == "not-expired" {
					expires = at.Add(time.Minute)
				}
				if mode == "foreign-scope" {
					s.Intermediate.Environment = "other"
				}
				current := deploymentplan.Object{"allow_expired_cert_revocation": false, "expiry": "72h", "disable": false, "auto_rebuild": true}
				before := deploymentplan.Clone(current)
				writes := 0
				api := func(_ context.Context, path string, body deploymentplan.Object) (deploymentplan.Object, error) {
					if path != s.Intermediate.Reference+"/config/crl" || strings.Contains(path, "*") {
						t.Fatal("unbound provider path", path)
					}
					if body != nil {
						writes++
						if len(body) != 1 || body["allow_expired_cert_revocation"] != true {
							t.Fatal("changed unrelated provider settings", body)
						}
						current["allow_expired_cert_revocation"] = true
						if mode == "unrelated-change" {
							current["expiry"] = "24h"
						}
						if mode == "lost-response" && writes == 1 {
							return nil, errors.New("lost native response")
						}
					}
					return deploymentplan.Clone(current), nil
				}
				run := func() error {
					return configureExpiredBootstrapRevocation(t.Context(), e, s, strings.Repeat("a", 64), expires, at, api)
				}
				err = run()
				if mode == "foreign-scope" || mode == "unrelated-change" {
					if err == nil {
						t.Fatal("invalid scope or unrelated configuration change accepted")
					}
					if mode == "foreign-scope" && writes != 0 {
						t.Fatal("foreign issuer mutated")
					}
					return
				}
				if mode == "lost-response" {
					if err == nil {
						t.Fatal("unknown response reported complete")
					}
					if err = run(); err != nil {
						t.Fatal("original observed write could not reconcile", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if err = run(); err != nil {
					t.Fatal("completed receipt could not be revalidated", err)
				}
				want := 1
				if mode == "not-expired" {
					want = 0
				} else {
					before["allow_expired_cert_revocation"] = true
				}
				if writes != want || deploymentplan.Digest(before) != deploymentplan.Digest(current) {
					t.Fatal("retry replayed mutation or changed unrelated configuration", writes)
				}
			})
		}
	}
}
