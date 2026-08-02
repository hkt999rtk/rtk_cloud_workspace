package main

import "testing"

func TestExpectedStatus(t *testing.T) {
	if !expectedStatus("401, 403", 403) {
		t.Fatal("403 should be accepted")
	}
	if expectedStatus("401,invalid", 500) {
		t.Fatal("500 must not be accepted")
	}
}
