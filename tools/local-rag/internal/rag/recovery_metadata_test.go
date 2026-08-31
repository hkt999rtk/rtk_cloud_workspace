package rag

import "testing"

func TestCoreRecoveryDocumentIsWorkspaceSource(t *testing.T) {
	classification, layer := ClassifyDocument("docs/backup-restore.md")
	if classification != "source" || layer != "workspace" {
		t.Fatalf("core recovery classification = %s/%s", classification, layer)
	}
}
