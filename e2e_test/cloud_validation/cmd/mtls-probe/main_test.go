package main

import "testing"

func TestExpectedStatus(t *testing.T) {
	if !expectedStatus("200, 204", 204) || expectedStatus("200", 403) {
		t.Fatal("unexpected accepted status result")
	}
}
