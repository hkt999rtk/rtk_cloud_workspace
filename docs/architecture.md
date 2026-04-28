# RTK Cloud Architecture Notes

This workspace tracks the integration boundary between the SDK client, video
cloud server, contracts repository, account manager, and MQTT support code.

## Source Of Truth

- Wire and payload contracts: `repos/rtk_cloud_contracts_doc`
- SDK/runtime implementation: `repos/rtk_cloud_client`
- Video server implementation: `repos/rtk_video_cloud`
- Account and registry implementation: `repos/rtk_account_manager`
- MQTT/broker support: `repos/rtk_mqtt`

## Boundary Rules

- `rtk_cloud_client` implements client SDK APIs and runtime behavior.
- `rtk_video_cloud` owns the video-cloud HTTP/WebSocket/MQTT server behavior.
- `rtk_account_manager` owns account, organization, and registry-only device
  behavior.
- Cross-service provisioning and channel behavior should be tracked as
  integration work, not hidden inside the SDK client.

