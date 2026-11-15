package deploymentplan

import (
	"errors"
	"strings"
	"testing"
)

type testAdmissionDiagnostic string

func (e testAdmissionDiagnostic) Error() string     { return "private raw error" }
func (e testAdmissionDiagnostic) CheckCode() string { return string(e) }
func TestAdmissionBlockerUsesOnlyAllowlistedCode(t *testing.T) {
	for _, tc := range []struct {
		code string
		want bool
	}{{"KUBE_IMMUTABLE", true}, {"KUBE_FORBIDDEN", true}, {"PRIVATE_TOKEN", false}} {
		err := admissionWriteBlocker("Job/ns/workload", testAdmissionDiagnostic(tc.code))
		var b *Blocker
		if !errors.As(err, &b) || b.Resource != "Job/ns/workload" || strings.Contains(b.Cause, "private raw") || strings.Contains(b.Cause, "PRIVATE_TOKEN") || strings.Contains(b.Cause, tc.code) != tc.want {
			t.Fatal("unsafe admission blocker", err)
		}
	}
}
