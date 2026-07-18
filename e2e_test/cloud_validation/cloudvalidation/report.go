package cloudvalidation

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func WriteReports(outDir string, report RunReport) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "results.json"), raw, 0o644); err != nil {
		return err
	}
	junit, err := renderJUnit(report)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "junit.xml"), junit, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "SUMMARY.md"), []byte(renderMarkdown(report)), 0o644)
}

type junitSuite struct {
	XMLName  xml.Name    `xml:"testsuite"`
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Time     string      `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Classname string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitOutcome `xml:"failure,omitempty"`
	Skipped   *junitOutcome `xml:"skipped,omitempty"`
}

type junitOutcome struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

func renderJUnit(report RunReport) ([]byte, error) {
	suite := junitSuite{
		Name: "sdk-cloud-validation-" + report.Platform,
		Time: fmt.Sprintf("%.3f", report.CompletedAt.Sub(report.StartedAt).Seconds()),
	}
	appendCase := func(classname, name string, status Status, durationMS int64, reasonCode, reason string) {
		item := junitCase{Classname: classname, Name: name, Time: fmt.Sprintf("%.3f", float64(durationMS)/1000)}
		message := reasonCode
		if safeReason := strings.TrimSpace(Redact(reason)); safeReason != "" {
			if message != "" {
				message += ": "
			}
			message += safeReason
		}
		switch status {
		case StatusFail:
			item.Failure = &junitOutcome{Message: message, Body: Redact(reason)}
			suite.Failures++
		case StatusSkip, StatusBlocked:
			item.Skipped = &junitOutcome{Message: string(status) + ": " + message}
			suite.Skipped++
		}
		suite.Cases = append(suite.Cases, item)
	}
	for _, step := range report.Steps {
		appendCase("cloud_validation.step", step.Name, step.Status, 0, step.ReasonCode, step.Reason)
	}
	if report.PlatformResult != nil {
		for _, result := range report.PlatformResult.Results {
			appendCase("cloud_validation.scenario", result.ScenarioID, result.Status, result.DurationMS, result.ReasonCode, result.Reason)
		}
	}
	suite.Tests = len(suite.Cases)
	raw, err := xml.MarshalIndent(suite, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(raw, '\n')...), nil
}

func renderMarkdown(report RunReport) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# SDK Cloud Validation Report")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Run ID: `%s`\n", report.RunID)
	fmt.Fprintf(&b, "- Environment: `%s`\n", report.Environment)
	fmt.Fprintf(&b, "- Platform: `%s`\n", report.Platform)
	fmt.Fprintf(&b, "- Mode: `%s`\n", report.Mode)
	fmt.Fprintf(&b, "- Overall: **%s**\n", report.Status)
	fmt.Fprintf(&b, "- SDK commit: `%s`\n", report.SDKCommit)
	fmt.Fprintf(&b, "- Contracts commit: `%s`\n", report.ContractsCommit)
	fmt.Fprintf(&b, "- Server version: `%s`\n", report.ServerVersion)
	fmt.Fprintf(&b, "- Brand Cloud: `%s`\n", report.BrandCloudSlug)
	fmt.Fprintf(&b, "- Host: `%s/%s` (`%s`)\n", report.Host["os"], report.Host["arch"], report.Host["hostname"])
	fmt.Fprintf(&b, "- Toolchains: Xcode `%s`; Swift `%s`; Android SDK `%s`; sdkmanager `%s`; Java `%s`; Gradle `%s`\n", report.Toolchains["xcode"], report.Toolchains["swift"], report.Toolchains["android_sdk"], report.Toolchains["sdkmanager"], report.Toolchains["java"], report.Toolchains["gradle"])
	fmt.Fprintf(&b, "- Run-scoped resources: `%d` (manifest: `%s`)\n", report.ResourceCount, report.ResourceManifest)
	fmt.Fprintf(&b, "- Result counts: PASS `%d`, FAIL `%d`, SKIP `%d`, BLOCKED `%d`\n", report.StatusCounts[StatusPass], report.StatusCounts[StatusFail], report.StatusCounts[StatusSkip], report.StatusCounts[StatusBlocked])
	if len(report.Artifacts) > 0 {
		fmt.Fprintln(&b, "- Evidence and diagnostic paths:")
		for _, name := range sortedMapKeys(report.Artifacts) {
			fmt.Fprintf(&b, "  - %s: `%s`\n", name, report.Artifacts[name])
		}
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Steps")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Step | Status | Reason code | Reason |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- |")
	for _, step := range report.Steps {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", md(step.Name), step.Status, md(step.ReasonCode), md(Redact(step.Reason)))
	}
	if report.PlatformResult != nil {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "## Scenarios")
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "| Scenario | Status | Duration ms | SDK error | Reason |")
		fmt.Fprintln(&b, "| --- | --- | ---: | --- | --- |")
		for _, result := range report.PlatformResult.Results {
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %s |\n", md(result.ScenarioID), result.Status, result.DurationMS, md(result.SDKErrorCode), md(Redact(result.Reason)))
		}
	}
	return b.String()
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func md(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}

func overallStatus(steps []StepResult, platform *PlatformResult) Status {
	status := StatusPass
	for _, step := range steps {
		switch step.Status {
		case StatusFail:
			return StatusFail
		case StatusBlocked:
			status = StatusBlocked
		}
	}
	if platform != nil {
		if platform.Status == StatusFail {
			return StatusFail
		}
		if platform.Status == StatusBlocked {
			status = StatusBlocked
		}
		if platform.Status == StatusSkip && status == StatusPass {
			status = StatusSkip
		}
	}
	return status
}
