package main

import "testing"

func TestPKIOperatorBindingRequiresSameEnvironmentIdentity(t *testing.T) {
	for _, tc := range []struct {
		name             string
		configured       string
		controller       string
		account          string
		configuredSigner string
		controllerSigner string
		wantError        bool
	}{
		{"legacy environment", "", "", "", "", "", false},
		{"matching operator", "operator-1", "operator-1", "operator-1", "offline:root", "offline:root", false},
		{"controller missing", "operator-1", "", "operator-1", "offline:root", "offline:root", true},
		{"Account Manager differs", "operator-1", "operator-1", "operator-2", "offline:root", "offline:root", true},
		{"signer differs", "operator-1", "operator-1", "operator-1", "offline:root", "offline:other", true},
		{"local record missing", "", "operator-1", "operator-1", "", "offline:root", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyPKIOperatorBinding(tc.configured, tc.controller, tc.account, tc.configuredSigner, tc.controllerSigner)
			if (err != nil) != tc.wantError {
				t.Fatalf("binding error=%v, wantError=%v", err, tc.wantError)
			}
		})
	}
}
