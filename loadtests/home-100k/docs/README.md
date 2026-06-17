# Home 100K Load-Test Documents

This directory is the canonical home for Home IoT loading-test documentation.
Do not add new Home 100K loading-test design, operation, report, scenario, or
legacy MQTT reference material under workspace-level `docs/` or script-level
READMEs.

## Documents

- `linode-100k-home-iot-shadow-loadtest.md`: operator guide for the Linode VM
  100K Home IoT Device Shadow workflow.
- `home-mqtt-loadtest-simulation.md`: lower-level home MQTT simulation
  reference used as background for the formal Home 100K runner.

## Related Package Files

- `../README.md`: package overview and source of truth for architecture and
  workflow.
- `../scenarios/default.description.env`: non-secret live/debug configuration.
- `../scripts/home-100k.sh`: public workflow script.
- `../scripts/generate-report.sh`: fixed-format report generator.
- `../reports/templates/TEST_REPORT.md.tmpl`: Markdown report template.
- `../reports/schema.md`: report and artifact schema.
