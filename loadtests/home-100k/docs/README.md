# Home 100K Load-Test Documents

This directory is the canonical home for Home IoT loading-test documentation.
Do not add new Home 100K loading-test design, operation, report, scenario, or
legacy MQTT reference material under workspace-level `docs/` or script-level
READMEs.

## Documents

- `prepare-another-machine.md`: operator onboarding for restoring or generating
  the git-ignored runtime prerequisites on another controller, and for allowing
  `workflow-live` to sync scoped inputs to load-generator VMs.
- `linode-100k-home-iot-shadow-loadtest.md`: operator guide for the Linode VM
  100K Home IoT Device Shadow workflow.
- `home-mqtt-loadtest-simulation.md`: lower-level home MQTT simulation
  reference used as background for the formal Home 100K runner.
- `../scenarios/mqtt-1k.description.env`: non-secret 1K MQTT/Device Shadow
  deployment-validation profile with the full `home-diverse-v1` device mix.
- `../scenarios/mqtt-canary.description.env`: ten-device correctness gate that
  must pass before the 1K Shadow qualification.
- `../scenarios/video-1k.description.env`: non-secret 1K Home + WebRTC pilot
  profile with 100 concurrent H264 relay sessions.
- `../scenarios/video-canary.description.env`: two-session WebRTC/TURN
  correctness gate.
- `../scenarios/clip-storage-canary.description.env` and
  `../scenarios/clip-storage-1k.description.env`: Clip lifecycle canary and
  one-thousand-upload feature qualification.
- `../scenarios/video-50k-turn.description.env`: non-secret 50K Home +
  relay-only WebRTC/TURN sizing profile.
- `../scenarios/video-100k-turn.description.env`: non-secret 100K Home +
  relay-only WebRTC/TURN sizing profile.

## Related Package Files

- `../README.md`: package overview and source of truth for architecture and
  workflow.
- `../scenarios/default.description.env`: non-secret live/debug configuration.
- `../scripts/home-100k.sh`: public workflow script.
- `../scripts/generate-report.sh`: fixed-format report generator.
- `../reports/templates/test_report.md.tmpl`: Markdown report template.
- `../reports/schema.md`: report and artifact schema, including two-host
  WebRTC ladder artifacts and TURN evidence gates.
