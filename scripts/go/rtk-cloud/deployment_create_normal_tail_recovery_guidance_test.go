package main

import (
	"strings"
	"testing"
	"time"
)

func TestCreateNormalTailRecoveryGuidanceUsesAdmission(t *testing.T) {
	now := time.Now().UTC()
	start, deadline := now.Add(-time.Minute), now.Add(time.Minute)
	err := environmentCreateBlocker("deployment.public-tls", "original DNS authorization is invalid")
	report := &unifiedDeploymentReport{ReusedPreflight: true, ExecutionAdmittedAt: &start, ExecutionDeadline: &deadline}
	row := createNormalTailProgramFailure(err, report, now)
	if row.ID != "deployment.public-tls" || row.Cause != "original DNS authorization is invalid" || !strings.Contains(row.Next, "same admitted plan") || !strings.Contains(row.Next, deadline.Format(time.RFC3339Nano)) || strings.Contains(row.Next, "repeat complete") {
		t.Fatal("admitted recovery was redirected to fresh preflight", row)
	}
	row = createNormalTailProgramFailure(err, report, deadline)
	if !strings.Contains(row.Next, "deadline has expired") || strings.Contains(row.Next, "resume the same") {
		t.Fatal("expired run was offered a retry", row)
	}
	row = createNormalTailProgramFailure(err, nil, now)
	if !strings.Contains(row.Next, "repeat complete create preflight") {
		t.Fatal("unadmitted qualification guidance changed", row)
	}
}
