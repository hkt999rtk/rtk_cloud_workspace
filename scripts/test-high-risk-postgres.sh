#!/usr/bin/env bash
set -euo pipefail

workspace_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
container_name="rtk-high-risk-postgres-$$"
image="${POSTGRES_TEST_IMAGE:-postgres:16-alpine}"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required for PostgreSQL integration tests" >&2
  exit 1
fi

cleanup() {
  docker rm --force "$container_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker run --detach --rm \
  --name "$container_name" \
  --label rtk.local-test=high-risk-postgres \
  --env POSTGRES_USER=postgres \
  --env POSTGRES_PASSWORD=local_test_only \
  --env POSTGRES_DB=postgres \
  --publish 127.0.0.1::5432 \
  "$image" >/dev/null

port=""
for attempt in $(seq 1 30); do
  port="$(docker port "$container_name" 5432/tcp | sed 's/.*://')"
  if docker exec "$container_name" pg_isready -U postgres -d postgres >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if [[ -z "$port" ]] || ! docker exec "$container_name" pg_isready -U postgres -d postgres >/dev/null 2>&1; then
  echo "temporary PostgreSQL did not become ready" >&2
  exit 1
fi

docker exec "$container_name" createdb -U postgres rtk_account_manager_test
docker exec "$container_name" createdb -U postgres rtk_billing_test
docker exec "$container_name" createdb -U postgres rtk_video_cloud_test

account_manager_dsn="postgres://postgres:local_test_only@127.0.0.1:${port}/rtk_account_manager_test?sslmode=disable"
billing_dsn="postgres://postgres:local_test_only@127.0.0.1:${port}/rtk_billing_test?sslmode=disable"
video_cloud_dsn="postgres://postgres:local_test_only@127.0.0.1:${port}/rtk_video_cloud_test?sslmode=disable"

echo "Running Account Manager identity, authorization, device claim, simulated lifecycle events, unprovision, collaborator, and Test Lab integration tests"
(
  cd "$workspace_root/repos/rtk_account_manager"
  GOWORK=off TEST_DATABASE_URL="$account_manager_dsn" go test -count=1 ./internal/api ./internal/store \
    -run '^(TestIntegrationEmailVerificationAndPasswordRecovery|TestIntegrationAuthorizationAndTenancyMatrix|TestIntegrationReadinessIgnoresOlderDeactivationGeneration|TestIntegrationProductCollaboratorLifecycleAndVisibility|TestLabConsoleIdentityAPI|TestLabDeviceAndSessionAPIHappyPath|TestTestLabBindingLifecycleIsolationAndRevocation|TestIntegrationClaimResolveEndpoint|TestIntegrationProvisioningEndpoints|TestIntegrationPlatformAdminDeviceItemProfileLifecycle|TestCreateOrGetDeviceOperationIsIdempotent|TestIntegrationDeviceUserUnprovisionWorkflow|TestIntegrationAdminDeviceUnprovisionOverride|TestIntegrationAdminDeviceClaimOverrideWorkflow|TestIntegrationInternalDeviceProvisioningResult|TestIntegrationDeactivateEndpointUsesProjectedVideoMetadata)$'
)

echo "Running cross-service Account Manager to Video Cloud activation delivery and replay integration test"
(
  cd "$workspace_root/repos/rtk_video_cloud"
  GOWORK=off VIDEO_CLOUD_TEST_DSN="$video_cloud_dsn" ACCOUNT_MANAGER_TEST_DSN="$account_manager_dsn" \
    go test -count=1 ./internal/httpapi -run '^TestIntegrationCrossRepositoryActivationReplay$'
)

echo "Running Billing invoice, payment authorization, webhook, ledger, reconciliation, refund, simulator, and idempotency integration tests"
(
  cd "$workspace_root/repos/rtk_billing"
  GOWORK=off TEST_DATABASE_URL="$billing_dsn" go test -count=1 ./internal/api ./internal/paymentstore ./internal/paymentservice \
    -run '^(TestIntegrationInternalBillingDebitAuthenticationAndIdempotency|TestIntegrationBillingHTTPPricingInvoiceAndTenantReadLifecycle|TestIntegrationBillingSummaryFailsClosedWithoutUsageEvidence|TestIntegrationBillingSummaryReportsForecastMeasurementBasis|TestIntegrationBillingActivityFailureProvidesSafeCodeActionAndReferences|TestIntegrationPaymentAPIAuthorizationLifecycleAndRedaction|TestIntegrationPaymentSimulatorHostedSetupActivatesMethodWithoutPersistingRawToken|TestIntegrationNewebPayHostedWebhookQueryCreditsExactlyOnce|TestLedgerTriggerAndIntentCreditAreExactlyOnce|TestTimeoutReconcilesToOneCredit|TestSimulatorChargeCreditsExactlyOnceAndRefundIsIdempotent|TestSimulatorUnknownChargeQueriesToConclusiveSuccess|TestSimulatorDeclineFailsWithoutCredit)$'
)

echo "High-risk PostgreSQL integration tests passed"
