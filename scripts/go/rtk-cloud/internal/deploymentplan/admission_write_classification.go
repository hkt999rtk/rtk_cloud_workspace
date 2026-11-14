package deploymentplan

import (
	"errors"
	"fmt"
)

func admissionWriteBlocker(resource string, err error) error {
	cause := "server dry-run rejected the exact desired body"
	var classified interface{ CheckCode() string }
	if errors.As(err, &classified) {
		switch code := classified.CheckCode(); code {
		case "KUBE_FORBIDDEN", "KUBE_UNAUTHORIZED", "KUBE_RATE_LIMITED", "KUBE_TIMEOUT", "KUBE_NOT_FOUND", "KUBE_UNAVAILABLE", "KUBE_TOOL_MISSING", "KUBE_INVALID_RESPONSE", "KUBE_COMMAND_FAILED", "KUBE_QUOTA", "KUBE_IMMUTABLE", "KUBE_ADMISSION_DENIED", "KUBE_INVALID", "KUBE_CONFLICT", "CHECK_CANCELLED":
			cause = fmt.Sprintf("%s [%s]", cause, code)
		}
	}
	return blocked("deployment.admission", resource, cause, "resolve write permission, immutable fields, admission, quota and namespace prerequisites")
}
