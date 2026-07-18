# Virtual-device adapter

`../scripts/run-virtual-device.sh` invokes the existing
`scripts/go/cloud-mqtt-test` process with load model `sdk-device-simulator`.
It is bounded to one through five run-scoped devices and writes `ready.json`
only after certificate-based token bootstrap, MQTT TLS connection, and shadow
delta subscription complete.

The deployed profile validates online desired/reported convergence. Nightly
uses a deterministic handshake: the device disconnects after `ready.json`, the
App writes a scenario-marked desired state through the SDK, a host controller
observes that state from Cloud, and only then creates the reconnect signal.
The device reconnects, reports the delta, and the App plus Cloud evidence both
verify convergence. A timing-only sleep is not accepted as proof.

`TestSDKDeviceSimulatorMatchesHome100KShadowDeviceContract` runs both adapters
against the same broker contract and verifies their delta subscription and
reported-state payload parity. Nightly also requires an invalid
device/certificate binding probe to receive HTTP 401/403 before valid device
startup. Protocol code must not be extracted into a reusable Go package until
this parity test remains green.
