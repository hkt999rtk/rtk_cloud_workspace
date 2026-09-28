#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'Usage: %s --stack STACK --workload cloud-admin|frontend --image IMAGE@sha256:DIGEST [--image-pull-secret NAME]\n' "$0" >&2
  exit 2
}

stack='' workload='' image='' pull_secret=''
while (($#)); do
  (($# >= 2)) || usage
  case "$1" in
    --stack) stack="$2" ;;
    --workload) workload="$2" ;;
    --image) image="$2" ;;
    --image-pull-secret) pull_secret="$2" ;;
    *) usage ;;
  esac
  shift 2
done
[[ "$stack" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || usage
[[ "$image" =~ ^[A-Za-z0-9._:/-]+@sha256:[a-fA-F0-9]{64}$ ]] || usage
[[ -z "$pull_secret" || "$pull_secret" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || usage

case "$workload" in
  cloud-admin)
    namespace="$stack-admin"; name=sqlite-migration-cloud-admin
    pvc=cloud-admin-sqlite-data; uid=10001; group=999 ;;
  frontend)
    namespace="$stack-frontend"; name=sqlite-migration-frontend
    pvc=frontend-sqlite-data; uid=100; group=101 ;;
  *) usage ;;
esac

cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $name
  namespace: $namespace
  labels:
    app.kubernetes.io/name: sqlite-migration-helper
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/stack: $stack
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  terminationGracePeriodSeconds: 10
  securityContext:
    fsGroup: $group
    fsGroupChangePolicy: OnRootMismatch
EOF
if [[ -n "$pull_secret" ]]; then
  printf '  imagePullSecrets:\n    - name: %s\n' "$pull_secret"
fi
cat <<EOF
  containers:
    - name: app
      image: $image
      imagePullPolicy: IfNotPresent
      command: ["sh", "-c", "sleep 86400"]
      securityContext:
        runAsNonRoot: true
        runAsUser: $uid
        runAsGroup: $group
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities:
          drop: ["ALL"]
      resources:
        requests:
          cpu: 50m
          memory: 64Mi
        limits:
          cpu: 500m
          memory: 256Mi
      volumeMounts:
        - name: sqlite-data
          mountPath: /migration
  volumes:
    - name: sqlite-data
      persistentVolumeClaim:
        claimName: $pvc
EOF
