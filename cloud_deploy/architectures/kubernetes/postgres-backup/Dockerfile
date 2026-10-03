ARG GO_IMAGE=golang:1.25-alpine
ARG POSTGRES_IMAGE
FROM ${GO_IMAGE} AS builder
WORKDIR /src/scripts/go
COPY scripts/go/go.mod scripts/go/go.sum ./
COPY repos/rtk_cloud_logger/go.mod repos/rtk_cloud_logger/go.sum /src/repos/rtk_cloud_logger/
RUN go mod download
COPY scripts/go/ ./
# Preserve the reviewed workspace replace path; no remote Logger fallback.
COPY repos/rtk_cloud_logger/ /src/repos/rtk_cloud_logger/
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -o /out/rtk-cloud ./rtk-cloud

# CI resolves POSTGRES_IMAGE to the same digest recorded in the backup config.
# Keeping the official PostgreSQL image supplies matching pg_basebackup,
# pg_verifybackup, pg_ctl and server binaries for isolated restore drills.
FROM ${POSTGRES_IMAGE}
ARG POSTGRES_IMAGE
ARG KUBECTL_VERSION=v1.34.3
ARG TARGETARCH=amd64
RUN apk add --no-cache ca-certificates curl \
    && test "$(id -u postgres)" = 70 \
    && postgres --version | grep -E '^postgres \(PostgreSQL\) 16\.' \
    && curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" -o /usr/local/bin/kubectl \
    && curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl.sha256" -o /tmp/kubectl.sha256 \
    && echo "$(cat /tmp/kubectl.sha256)  /usr/local/bin/kubectl" | sha256sum -c - \
    && chmod 0755 /usr/local/bin/kubectl \
    && rm /tmp/kubectl.sha256
COPY --from=builder /out/rtk-cloud /usr/local/bin/rtk-cloud
LABEL org.opencontainers.image.title="rtk-postgres-backup" \
      org.opencontainers.image.base.name="${POSTGRES_IMAGE}"
ENV RTK_POSTGRES_RUNNER_SOURCE_IMAGE=${POSTGRES_IMAGE}
USER 70:70
ENTRYPOINT ["/usr/local/bin/rtk-cloud"]
CMD ["postgres-backup", "--help"]
