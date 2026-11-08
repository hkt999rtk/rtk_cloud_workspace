package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyInventoryRecordsActualEnrollmentSubjectsAndPaths(t *testing.T) {
	cfg, p, runtime, service := createServiceRecipeFixture(t, "staging")
	service.BootstrapCredentialNamespaces = append(service.BootstrapCredentialNamespaces, p.Stack+"-secrets")
	owners, err := environmentCustodyOwners(cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := renderEnvironmentCreateServiceEnrollmentDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	apps, err := renderEnvironmentCreateApplicationEnrollment(cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	transports, err := renderEnvironmentCreateTransportClientDefinitions(context.Background(), cfg, p, runtime, service, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	definitions = append(definitions, apps...)
	definitions = append(definitions, transports...)
	checked := 0
	for _, definition := range definitions {
		if definition.Object["kind"] != "Job" {
			continue
		}
		pod := environmentPKIPod(definition.Object)
		container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		command := certIssuerObjectList(container["command"])
		if len(command) != 2 || !strings.HasPrefix(deploymentplan.String(command[1]), "service:") {
			continue
		}
		claim := deploymentplan.String(deploymentplan.Map(managedNamedField(pod["volumes"], "service-state")["persistentVolumeClaim"])["claimName"])
		found := false
		for _, owner := range owners {
			for _, alias := range owner.StorageAliases {
				if alias != claim || owner.Namespace != deploymentplan.Map(definition.Object["metadata"])["namespace"] {
					continue
				}
				encoded, err := json.Marshal(owner)
				if err != nil {
					t.Fatal(err)
				}
				raw := jsonRawObject(encoded)
				for _, value := range certIssuerObjectList(raw["clients"]) {
					client := deploymentplan.Map(value)
					found = found || client["subject"] == command[1] && client["state_path"] == mqttFoundationEnv(container, "PKI_CLIENT_IDENTITY_STATE")
				}
			}
		}
		if claim == "openbao-pki-identity" {
			if found {
				t.Fatal("OpenBao cold-start key was included in remote custody")
			}
			continue
		}
		checked++
		if !found {
			t.Fatalf("original inventory lost actual client recipe %s at %s", command[1], claim)
		}
	}
	if checked < 10 {
		t.Fatalf("only checked %d client recipes", checked)
	}
}

func TestCustodyClientPurposeValidationRejectsAmbiguousOrForeignSubjects(t *testing.T) {
	for _, clients := range [][]environmentCustodyClientBinding{
		{{Subject: "service:api", StatePath: "relative.json"}},
		{{Subject: "service:api", StatePath: "/identity/../client.json"}},
		{{Subject: "device:api", StatePath: "/identity/client.json"}},
		{{Subject: "service:api';DELETE", StatePath: "/identity/client.json"}},
		{{Subject: "service:api", StatePath: "/identity/client.json"}, {Subject: "service:other", StatePath: "/identity/client.json"}},
	} {
		binding := environmentIdentityBindings{Version: 1, Environment: "dev", Stack: "video-cloud-dev", Custody: &environmentCustodyBinding{
			Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64),
			Owners: []environmentCustodyOwnerBinding{{ID: "api", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "api", Clients: clients}},
		}}
		if err := validateEnvironmentIdentityBindings(binding, "dev"); err == nil {
			t.Fatalf("accepted invalid original enrollment purposes: %#v", clients)
		}
	}
}
