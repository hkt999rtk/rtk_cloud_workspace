package main

import "io"

func deploymentStatusSuccessful(status string) bool {
	return status == "PASS" || status == "OK"
}

// Display labels do not change the statuses stored in evidence and JSON reports.
func deploymentStatusLabel(out io.Writer, status string) string {
	color := false
	if output, ok := out.(interface{ deploymentColorEnabled() bool }); ok {
		color = output.deploymentColorEnabled()
	} else {
		_, _, color = environmentProgressTerminal(out)
	}
	return deploymentStatusLabelColor(status, color)
}

func deploymentStatusLabelColor(status string, color bool) string {
	code := ""
	switch status {
	case "PASS", "OK":
		status, code = "OK", "32"
	case "BLOCKED", "FAIL", "ERROR":
		code = "31"
	}
	label := "[" + status + "]"
	if color && code != "" {
		return "\x1b[" + code + "m" + label + "\x1b[0m"
	}
	return label
}
