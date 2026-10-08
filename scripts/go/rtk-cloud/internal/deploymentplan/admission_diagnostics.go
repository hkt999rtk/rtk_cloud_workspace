package deploymentplan

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const admissionDiagnosticLimit = 8
const admissionDiagnosticPathLimit = 192
const admissionDiagnosticNodeLimit = 8192

// Only fixed schema field names can appear in public diagnostics. Dynamic maps
// and unknown fields use a wildcard; names, values and server output never do.
var admissionDiagnosticFields = func() map[string]bool {
	fields := strings.Fields(`apiVersion kind metadata name namespace uid labels annotations
spec template selector matchLabels matchExpressions key operator values replicas
containers initContainers ephemeralContainers env envFrom value valueFrom
secretKeyRef configMapKeyRef fieldRef resourceFieldRef secretRef configMapRef
image imagePullPolicy imagePullSecrets command args workingDir ports containerPort
hostPort protocol resources requests limits volumeMounts mountPath subPath readOnly
volumes secret configMap projected sources items path defaultMode persistentVolumeClaim
claimName emptyDir medium sizeLimit hostPath type securityContext runAsUser runAsGroup
runAsNonRoot fsGroup fsGroupChangePolicy supplementalGroups allowPrivilegeEscalation
privileged readOnlyRootFilesystem capabilities add drop seccompProfile profile
serviceAccountName automountServiceAccountToken nodeSelector affinity tolerations
nodeAffinity podAffinity podAntiAffinity requiredDuringSchedulingIgnoredDuringExecution
preferredDuringSchedulingIgnoredDuringExecution weight preference topologyKey
readinessProbe livenessProbe startupProbe httpGet tcpSocket exec grpc port scheme
initialDelaySeconds periodSeconds timeoutSeconds failureThreshold successThreshold
terminationGracePeriodSeconds restartPolicy dnsPolicy hostNetwork schedulingGates
strategy rollingUpdate maxSurge maxUnavailable updateStrategy podManagementPolicy
serviceName volumeClaimTemplates accessModes storageClassName volumeName volumeMode
data binaryData stringData immutable finalizers ownerReferences controller blockOwnerDeletion
rules verbs apiGroups apiResources resourceNames subjects roleRef rules ingress egress
from to podSelector namespaceSelector ipBlock cidr except policyTypes tls hosts host
secretName backend service number className ingressClassName clusterIP clusterIPs
ipFamilies ipFamilyPolicy externalTrafficPolicy internalTrafficPolicy targetPort
sessionAffinity healthCheckNodePort externalIPs loadBalancerIP publishNotReadyAddresses
schedule timeZone concurrencyPolicy suspend jobTemplate successfulJobsHistoryLimit
failedJobsHistoryLimit completions parallelism backoffLimit activeDeadlineSeconds
ttlSecondsAfterFinished minAvailable maxUnavailable maxReplicas minReplicas metrics
webhooks clientConfig url caBundle admissionReviewVersions sideEffects failurePolicy
matchPolicy reinvocationPolicy objectSelector scope operations generation`)
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	return allowed
}()

func admissionMismatchCause(cause string, body, admitted Object) string {
	paths := admissionMismatchPaths(body, admitted)
	if len(paths) == 0 {
		return cause + "; mismatch paths unavailable"
	}
	return cause + "; mismatch paths: " + strings.Join(paths, ", ")
}

// Traverse the same desired subset as Matches, after the same cloned admission
// normalization. Missing and null retain their existing comparison semantics.
func admissionMismatchPaths(body, admitted Object) []string {
	want, have, normalized := admissionProjections(body, admitted)
	if want == nil || have == nil {
		return nil
	}
	paths := map[string]bool{}
	add := func(path string) {
		if len(paths) < admissionDiagnosticLimit && len(path) <= admissionDiagnosticPathLimit {
			paths[path] = true
		}
	}
	if uid := String(Map(body["metadata"])["uid"]); uid != "" && uid != String(Map(admitted["metadata"])["uid"]) {
		add("$.metadata.uid")
	}
	if !normalized {
		add("$.metadata.annotations.*")
		add("$.metadata.generation")
	}
	budget := admissionDiagnosticNodeLimit
	bounded := true
	var visit func(any, any, string, int, bool)
	visit = func(expected, actual any, path string, depth int, private bool) {
		if !bounded || len(paths) == admissionDiagnosticLimit {
			return
		}
		budget--
		if budget < 0 || depth > 32 || len(path) > admissionDiagnosticPathLimit {
			bounded = false
			return
		}
		switch expected := expected.(type) {
		case map[string]any:
			actual, ok := actual.(map[string]any)
			if !ok {
				add(path)
				return
			}
			if len(expected) > budget {
				bounded = false
				return
			}
			// Compare dynamic maps with the same bounded traversal, but keep
			// every nested difference at one wildcard path. Calling subset
			// here would recursively compare before these bounds were applied.
			if !private {
				field := path[strings.LastIndex(path, ".")+1:]
				switch field {
				case "data", "binaryData", "stringData", "labels", "annotations", "matchLabels", "nodeSelector", "requests", "limits":
					path += ".*"
					private = true
				}
			}
			keys := make([]string, 0, len(expected))
			for key := range expected {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				next, masked := path, private
				if !private {
					if admissionDiagnosticFields[key] {
						next += "." + key
					} else {
						next += ".*"
						masked = true
					}
				}
				visit(expected[key], actual[key], next, depth+1, masked)
				if !bounded || len(paths) == admissionDiagnosticLimit {
					return
				}
			}
		case []any:
			actual, ok := actual.([]any)
			if !ok || len(expected) != len(actual) {
				add(path)
				return
			}
			if len(expected) > budget {
				bounded = false
				return
			}
			for index, value := range expected {
				next := path
				if !private {
					next += "[" + strconv.Itoa(index) + "]"
				}
				visit(value, actual[index], next, depth+1, private)
				if !bounded || len(paths) == admissionDiagnosticLimit {
					return
				}
			}
		default:
			if !reflect.DeepEqual(expected, actual) {
				add(path)
			}
		}
	}
	if len(want) > budget {
		return nil
	}
	keys := make([]string, 0, len(want))
	for key := range want {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "metadata" {
			for _, field := range []string{"name", "namespace", "labels", "annotations"} {
				if expected, ok := Map(want[key])[field]; ok {
					visit(expected, Map(have[key])[field], "$.metadata."+field, 0, false)
				}
			}
			continue
		}
		field := "*"
		if admissionDiagnosticFields[key] {
			field = key
		}
		visit(want[key], have[key], "$."+field, 0, field == "*")
		if !bounded || len(paths) == admissionDiagnosticLimit {
			break
		}
	}
	if !bounded {
		return nil
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}
