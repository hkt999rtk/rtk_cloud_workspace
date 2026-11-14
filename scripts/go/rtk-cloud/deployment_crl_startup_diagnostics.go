package main

import (
	"path/filepath"
	"strings"
)

// Only the fixed native startup command's own failed execution is classified.
// Original stderr stays private, including database URLs and filesystem paths.
func classifyOpenBaoStartupCommandError(args []string, raw []byte) *deploymentRuntimeError {
	if len(args) < 4 || args[len(args)-3] != "--" || filepath.Base(args[len(args)-2]) != "openbaopkihost" || args[len(args)-1] != "prepare-startup" {
		return nil
	}
	diagnostic := strings.ToLower(string(raw))
	if strings.Contains(diagnostic, "exec failed") && strings.Contains(diagnostic, "no such file") {
		return &deploymentRuntimeError{"OPENBAO_STARTUP_TOOL_MISSING", "native OpenBao startup command is absent from the selected container"}
	}
	if !strings.Contains(diagnostic, "command terminated with exit code") {
		return nil
	}
	code, detail := "OPENBAO_STARTUP_NATIVE", "native OpenBao startup trust preparation failed"
	switch {
	case strings.Contains(diagnostic, "original registry admission failed"):
		code, detail = "OPENBAO_STARTUP_REGISTRY", "original OpenBao server identity could not be admitted by the registry"
	case strings.Contains(diagnostic, "original startup issuer unavailable"):
		code, detail = "OPENBAO_STARTUP_ISSUER", "original OpenBao startup issuer is unavailable in the registry"
	case strings.Contains(diagnostic, "original startup crl unavailable"):
		code, detail = "OPENBAO_STARTUP_CRL_MISSING", "original OpenBao startup CRL is unavailable in the registry"
	case strings.Contains(diagnostic, "crl"):
		code, detail = "OPENBAO_STARTUP_CRL_INVALID", "native OpenBao startup CRL validation failed"
	case strings.Contains(diagnostic, "permission denied"):
		code, detail = "OPENBAO_STARTUP_PERMISSION", "native OpenBao startup trust preparation lacks access to its original state"
	case strings.Contains(diagnostic, "identity"):
		code, detail = "OPENBAO_STARTUP_IDENTITY", "native OpenBao original server identity validation failed"
	case strings.Contains(diagnostic, "deadline exceeded"), strings.Contains(diagnostic, "timeout"), strings.Contains(diagnostic, "timed out"):
		code, detail = "OPENBAO_STARTUP_TIMEOUT", "native OpenBao startup trust preparation exceeded its deadline"
	}
	return &deploymentRuntimeError{code, detail}
}
