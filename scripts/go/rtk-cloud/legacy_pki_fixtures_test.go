package main

import "fmt"

// Historical samples for route regression tests. No runtime renderer uses them.
func historicalLkeCertIssuerDeploymentManifest(env map[string]string, material lkeCertIssuerMaterial, openBao lkeOpenBaoBootstrapResult) string {
	tlsConfig := lkeResolveCertIssuerTLSConfig(env)
	checksum := lkeConfigChecksum(
		material.ServerCert,
		material.ServiceCA,
		material.RootCACert,
		material.DeviceCACert,
		material.AppCACert,
		openBao.RoleID,
		openBao.SecretID,
		openBao.TLSCACert,
		lkeRuntimeSecretValue("postgres"),
	)
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: certissuer
  namespace: %s
  labels:
    app.kubernetes.io/name: certissuer
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: certissuer
  template:
    metadata:
      annotations:
        rtk.realtek.com/runtime-checksum: %q
      labels:
        app.kubernetes.io/name: certissuer
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      imagePullSecrets:
        - name: %s
      containers:
        - name: certissuer
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/certissuer"]
          ports:
            - name: https
              containerPort: %d
          env:
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: certissuer-runtime
                  key: POSTGRES_PASSWORD
            - name: CERT_ISSUER_LISTEN_ADDR
              value: ":%d"
            - name: CERT_ISSUER_SERVER_CERT
              value: /etc/video-cloud/certissuer/tls.crt
            - name: CERT_ISSUER_SERVER_KEY
              value: /etc/video-cloud/certissuer/tls.key
            - name: CERT_ISSUER_CLIENT_CA
              value: /etc/video-cloud/certissuer/client-ca.crt
            - name: CERT_ISSUER_CA_CERT_PATH
              value: /etc/video-cloud/certissuer/device-ca.crt
            - name: CERT_ISSUER_APP_CA_CERT_PATH
              value: /etc/video-cloud/certissuer/app-ca.crt
            - name: CERT_ISSUER_SIGNER_PROVIDER
              value: openbao
            - name: CERT_ISSUER_OPENBAO_PKI_MOUNT
              value: pki/device
            - name: CERT_ISSUER_OPENBAO_PKI_ROLE
              value: factory-device
            - name: CERT_ISSUER_OPENBAO_GATEWAY_ROLE
              value: gateway-server
            - name: CERT_ISSUER_APP_SIGNER_PROVIDER
              value: openbao
            - name: CERT_ISSUER_APP_OPENBAO_PKI_MOUNT
              value: pki/app
            - name: CERT_ISSUER_APP_OPENBAO_PKI_ROLE
              value: app-user
            # Existing LKE environments can still hold the pre-managed-PKI
            # account-manager client certificate. New material uses the
            # service:account-manager identity from the PKI design.
            - name: CERT_ISSUER_APP_CLIENT_CN_PATTERN
              value: "^(account-manager|service:account-manager)$"
            - name: OPENBAO_ADDR
              value: %q
            - name: OPENBAO_CACERT
              value: /etc/video-cloud/openbao/ca.crt
            - name: OPENBAO_AUTH_METHOD
              value: approle
            - name: OPENBAO_ROLE_ID_FILE
              value: /etc/video-cloud/openbao/role_id
            - name: OPENBAO_SECRET_ID_FILE
              value: /etc/video-cloud/openbao/secret_id
            - name: CERT_ISSUER_DB_DSN
              value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
          volumeMounts:
            - name: certissuer-runtime
              mountPath: /etc/video-cloud/certissuer
              readOnly: true
            - name: certissuer-openbao-auth
              mountPath: /etc/video-cloud/openbao
              readOnly: true
      volumes:
        - name: certissuer-runtime
          secret:
            secretName: certissuer-runtime
        - name: certissuer-openbao-auth
          secret:
            secretName: certissuer-openbao-auth
`, tlsConfig.Namespace, env["CLOUD_STACK_NAME"], checksum, env["CLOUD_STACK_NAME"], lkeImagePullSecretName(env), lkeVideoCloudImage(env), tlsConfig.HTTPSPort, tlsConfig.HTTPSPort, lkeOpenBaoAddr(env), lkeNamespaceName(env, "platform"))
}

func historicalLkeFactoryEnrollDeploymentManifest(env map[string]string, material lkeCertIssuerMaterial) string {
	checksum := lkeConfigChecksum(
		lkeRuntimeSecretValue("postgres"),
		lkeRuntimeSecretValue("factory-enroll-auth"),
		lkeFactoryProductionJWTSecret(env),
		lkeFactoryProductionJWTAudience(env),
		lkeRuntimeSecretValue("factory-admission"),
		lkeFactoryAdmissionBaseURL(env),
		lkeHandoffRuntimeValue(env, lkeFactoryHandoffToken()),
		lkeCertIssuerBaseURL(env),
		material.FactoryCert,
		material.ServiceCA,
	)
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: factoryenroll
  namespace: %s
  labels:
    app.kubernetes.io/name: factoryenroll
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: factoryenroll
  template:
    metadata:
      annotations:
        rtk.realtek.com/runtime-checksum: %q
      labels:
        app.kubernetes.io/name: factoryenroll
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      imagePullSecrets:
        - name: %s
      containers:
        - name: factoryenroll
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/factoryenroll"]
          ports:
            - name: http
              containerPort: 18443
          env:
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: factoryenroll-runtime
                  key: POSTGRES_PASSWORD
            - name: FACTORY_ENROLL_AUTH_KEY
              valueFrom:
                secretKeyRef:
                  name: factoryenroll-runtime
                  key: FACTORY_ENROLL_AUTH_KEY
            - name: FACTORY_ENROLL_PRODUCTION_JWT_SECRET
              valueFrom:
                secretKeyRef:
                  name: factoryenroll-runtime
                  key: FACTORY_ENROLL_PRODUCTION_JWT_SECRET
            - name: FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE
              valueFrom:
                secretKeyRef:
                  name: factoryenroll-runtime
                  key: FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE
            - name: FACTORY_ENROLL_ACCOUNT_MANAGER_URL
              value: %q
            - name: FACTORY_ENROLL_ACCOUNT_MANAGER_TOKEN
              valueFrom:
                secretKeyRef:
                  name: factoryenroll-runtime
                  key: FACTORY_ENROLL_ACCOUNT_MANAGER_TOKEN
            - name: FACTORY_ENROLL_RECOVERY_TOKEN
              valueFrom:
                secretKeyRef:
                  name: factoryenroll-runtime
                  key: FACTORY_ENROLL_RECOVERY_TOKEN
            - name: FACTORY_ENROLL_ADDR
              value: ":18443"
            - name: VIDEO_CLOUD_ENV
              value: "staging"
            - name: FACTORY_ENROLL_CERT_ISSUER_URL
              value: %q
            - name: FACTORY_ENROLL_CERT_ISSUER_CLIENT_CERT
              value: /etc/video-cloud/factoryenroll/client.crt
            - name: FACTORY_ENROLL_CERT_ISSUER_CLIENT_KEY
              value: /etc/video-cloud/factoryenroll/client.key
            - name: FACTORY_ENROLL_CERT_ISSUER_CA
              value: /etc/video-cloud/factoryenroll/ca.crt
            - name: VIDEO_CLOUD_DB_DSN
              value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
          volumeMounts:
            - name: factoryenroll-certissuer-client
              mountPath: /etc/video-cloud/factoryenroll
              readOnly: true
      volumes:
        - name: factoryenroll-certissuer-client
          secret:
            secretName: factoryenroll-certissuer-client
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], checksum, env["CLOUD_STACK_NAME"], lkeImagePullSecretName(env), lkeVideoCloudImage(env), lkeFactoryAdmissionBaseURL(env), lkeCertIssuerBaseURL(env), lkeNamespaceName(env, "platform"))
}
