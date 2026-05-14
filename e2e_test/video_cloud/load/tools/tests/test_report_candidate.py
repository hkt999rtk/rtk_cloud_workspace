import tempfile
import unittest
from pathlib import Path
import sys

LOAD_ROOT = Path(__file__).resolve().parents[2]
WORKSPACE_ROOT = Path(__file__).resolve().parents[5]
sys.path.insert(0, str(LOAD_ROOT))

from tools import report_candidate


REPO_ROOT = WORKSPACE_ROOT


class ReportCandidateTests(unittest.TestCase):
    def test_report_type_maps_to_allowlisted_canonical_path(self) -> None:
        self.assertEqual(
            report_candidate.canonical_path_for("LOAD_TEST_REPORT"),
            Path("docs/LOAD_TEST_REPORT.md"),
        )
        with self.assertRaisesRegex(report_candidate.ReportCandidateError, "not allowlisted"):
            report_candidate.canonical_path_for("TEST_REPORT")

    def test_validate_rejects_non_allowlisted_paths_and_secret_like_content(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            candidate = root / ".artifacts/report-candidates/docs/LOAD_TEST_REPORT.md"
            candidate.parent.mkdir(parents=True)
            candidate.write_text(
                "# rtk_cloud_workspace E2E Load Test Report\n\n"
                "## Summary\n\n"
                "- Overall result: PASS\n\n"
                "## Source Anchors\n\n"
                "## Environment\n\n"
                "Authorization: Bearer secret-token\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(report_candidate.ReportCandidateError, "secret-like"):
                report_candidate.validate_candidate(
                    report_type="LOAD_TEST_REPORT",
                    candidate_path=candidate,
                    canonical_path=root / "docs/LOAD_TEST_REPORT.md",
                )

            with self.assertRaisesRegex(report_candidate.ReportCandidateError, "expected canonical"):
                report_candidate.validate_candidate(
                    report_type="LOAD_TEST_REPORT",
                    candidate_path=candidate,
                    canonical_path=root / "docs/OTHER.md",
                )

    def test_build_candidate_wraps_source_report_without_copying_raw_logs(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "local.md"
            output = root / ".artifacts/report-candidates/docs/LOAD_TEST_REPORT.md"
            source.write_text("# Raw Report\n\n## Result Summary\n\nPASS\n", encoding="utf-8")

            report_candidate.build_candidate(
                report_type="LOAD_TEST_REPORT",
                output_path=output,
                repository="hkt999rtk/rtk_cloud_workspace",
                report_id="ci-123",
                source_report=source,
                source_artifact="video-loadtest-local-report",
                repository_commit="abc123",
                contracts_commit="def456",
                prepared_by="github-actions[bot]",
                automation_identity="CI",
                report_class="PR validation",
                overall_result="PASS",
                scope="Tier 0 deterministic validation",
            )

            text = output.read_text(encoding="utf-8")
            self.assertIn("# rtk_cloud_workspace E2E Load Test Report", text)
            self.assertIn("- Report ID: ci-123", text)
            self.assertIn("- Repository commit: abc123", text)
            self.assertIn("- Contracts commit: def456", text)
            self.assertIn("video-loadtest-local-report", text)
            self.assertIn("<details>", text)
            self.assertNotIn("Bearer secret-token", text)

    def test_checked_in_canonical_reports_are_valid_placeholders(self) -> None:
        for report_type, canonical in report_candidate.ALLOWLISTED_REPORTS.items():
            report_candidate.validate_candidate(
                report_type=report_type,
                candidate_path=REPO_ROOT / canonical,
                canonical_path=REPO_ROOT / canonical,
            )


if __name__ == "__main__":
    unittest.main()
