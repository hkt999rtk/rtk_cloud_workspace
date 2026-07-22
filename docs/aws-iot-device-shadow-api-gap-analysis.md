# AWS IoT Device Shadow API Gap Analysis

Date: 2026-07-22

Status: implementation review and alignment recommendation

## Executive summary

The RTK Device Shadow implementation follows the same basic model as AWS IoT
Device Shadow: unnamed and named shadows, `desired` and `reported` state,
computed `delta`, partial object merge, deletion by `null`, monotonically
increasing versions, client-token correlation, and MQTT
`accepted`/`rejected`/`delta`/`documents` notifications.

It is **not AWS API compatible today**. An AWS Device SDK cannot be pointed at
RTK unchanged because the HTTP paths, MQTT topic prefix, authentication, list
operation, validation, and several response documents differ. More importantly,
there are behavioral differences that can break a client even after topic/path
translation:

1. Redis update and version comparison are not atomic, so concurrent updates
   can lose state and return duplicate versions.
2. `update/accepted`, `update/documents`, metadata, and delete responses do not
   have AWS shapes.
3. AWS validation limits are not enforced, including the 64-byte client token,
   shadow/thing name grammar, 8 KiB state document, nesting limit, and arrays
   containing `null`.
4. RTK lifecycle code automatically creates an unnamed shadow; AWS creates no
   shadow until the first explicit update.
5. RTK tombstones retain the version boundary indefinitely; AWS resets the
   version when a deleted shadow is recreated after 48 hours.
6. The RTK HTTP desired/reported authorization rule is a product extension, not
   an AWS API semantic, and the same rule is not enforceable by the current
   shared MQTT `update` topic alone.

Recommendation: define an explicit **AWS-compatible v1 mode**, preserve `$vc`
and `/api/devices/...` as legacy aliases during migration, and make the
transport-neutral service semantics conform before adding AWS path/topic
aliases.

## Scope and evidence

The RTK findings are based on these workspace sources:

- [`repos/rtk_cloud_contracts_doc/DEVICE_SHADOW.md`](../repos/rtk_cloud_contracts_doc/DEVICE_SHADOW.md)
- [`repos/rtk_video_cloud/docs/device-shadow-spec.md`](../repos/rtk_video_cloud/docs/device-shadow-spec.md)
- [`repos/rtk_video_cloud/internal/deviceshadow/service.go`](../repos/rtk_video_cloud/internal/deviceshadow/service.go)
- [`repos/rtk_video_cloud/internal/httpapi/device_shadow.go`](../repos/rtk_video_cloud/internal/httpapi/device_shadow.go)
- [`repos/rtk_video_cloud/internal/mqtt/shadow.go`](../repos/rtk_video_cloud/internal/mqtt/shadow.go)
- [`repos/rtk_video_cloud/internal/rediscache/deviceshadow.go`](../repos/rtk_video_cloud/internal/rediscache/deviceshadow.go)

AWS behavior is based on the current official documentation:

- [Device Shadow service documents](https://docs.aws.amazon.com/iot/latest/developerguide/device-shadow-document.html)
- [Device Shadow MQTT topics](https://docs.aws.amazon.com/iot/latest/developerguide/device-shadow-mqtt.html)
- [Device Shadow REST API](https://docs.aws.amazon.com/iot/latest/developerguide/device-shadow-rest-api.html)
- [Interacting with shadows](https://docs.aws.amazon.com/iot/latest/developerguide/device-shadow-data-flow.html)
- [Device Shadow error messages](https://docs.aws.amazon.com/iot/latest/developerguide/device-shadow-error-messages.html)
- [IoT Core Device Shadow quotas](https://docs.aws.amazon.com/general/latest/gr/iot-core.html#iot-protocol-limits)
- [ListNamedShadowsForThing](https://docs.aws.amazon.com/iot/latest/apireference/API_iotdata_ListNamedShadowsForThing.html)

This is a source-level comparison. It does not claim that RTK staging was
probed against a live AWS account.

## Compatibility verdict

| Area | RTK compared with AWS | Impact |
| --- | --- | --- |
| Desired/reported model | Mostly aligned | Low |
| Partial merge and `null` deletion | Mostly aligned | Low |
| Delta calculation | Aligned for ordinary JSON objects and arrays | Low |
| Version intent | Aligned | Low |
| Version implementation under concurrency | Not aligned | Critical |
| MQTT topic suffixes | Aligned | Low |
| MQTT topic prefix | Different: `$vc/devices/...` vs `$aws/things/...` | High |
| HTTP data-plane paths | Different | High |
| HTTP/MQTT response shapes | Partially different | High |
| Nested metadata shape | Different | High |
| Delete/recreate lifecycle | Different | High |
| Validation and quotas | Substantially different | High |
| Named-shadow listing | Different and incomplete | Medium |
| Authentication/authorization | Product-specific | Medium |
| Lifecycle bootstrap | RTK extension | Medium |

## Detailed semantic gaps

### 1. Concurrent updates do not provide AWS-style version semantics

AWS increments the document version for every accepted update and rejects an
update whose supplied version does not match the current version. This requires
serialization or compare-and-swap at the storage boundary.

RTK performs `Get`, compares `req.Version`, increments in process, and then
calls `Save`. The Redis store implements `Save` as an independent `SET`. Two
handlers can therefore read version 10, both pass a request version of 10, both
produce version 11, and the last `SET` wins. Even without a supplied version,
two partial patches can overwrite each other.

This is more than an implementation detail: clients can observe successful
responses for state that was lost, duplicate version numbers, or a missing
`documents` transition.

**Alignment requirement:** move read, version validation, merge, version
increment, tombstone handling, document write, and named-index maintenance into
one atomic Redis Lua/function operation (or an equivalent transaction with
retry). Return the previous and current documents from that operation so MQTT
notifications describe the committed transition.

### 2. Wire routes and topics differ

AWS data-plane HTTP:

```text
GET    /things/{thingName}/shadow?name={shadowName}
POST   /things/{thingName}/shadow?name={shadowName}
DELETE /things/{thingName}/shadow?name={shadowName}
GET    /api/things/shadow/ListNamedShadowsForThing/{thingName}?pageSize=...&nextToken=...
```

RTK HTTP:

```text
GET    /api/devices/{devid}/shadow?name={shadowName}
POST   /api/devices/{devid}/shadow?name={shadowName}
DELETE /api/devices/{devid}/shadow?name={shadowName}
GET    /api/devices/{devid}/shadows
```

AWS MQTT uses `$aws/things/{thingName}/shadow...`; RTK uses
`$vc/devices/{devid}/shadow...`. The action suffixes otherwise match.

**Alignment requirement:** add AWS-compatible aliases. Keep the existing names
only as a documented RTK extension during a deprecation window. Internally map
`thingName` to the existing canonical `devid`; do not duplicate shadow state.

### 3. Update response documents differ

AWS `update/accepted` (and the UpdateThingShadow HTTP response) describes the
accepted part of the update: only the `desired` and/or `reported` fields in the
request, their metadata, response timestamp, client token when supplied, and
the new version.

RTK returns a complete document with all `desired`, `reported`, and `delta`
fields on every update. It also always emits empty state sections and includes
the RTK-only `updated_at` field.

An AWS client may use `update/accepted` as a patch acknowledgement and use
`get/accepted` for the full document. RTK's larger response can change that
client's state or fail strict schema tests.

**Alignment requirement:** construct the accepted response from the normalized
request patch, not the full stored document. Continue returning the complete
document for `get/accepted`. Omit absent/empty state sections as AWS does. If
`updated_at` is retained, place it behind an RTK extension mode rather than in
AWS-compatible payloads.

### 4. `update/documents` shape differs

AWS places full `state`, `metadata`, and `version` under `previous` and
`current`, with `timestamp` and optional `clientToken` at the outer level.

RTK reuses its generic full-document serializer under `previous` and `current`.
Consequently the nested objects also contain `delta`, `timestamp`,
`clientToken`, and `updated_at`; RTK additionally adds an outer `version` that
is not part of the AWS documented shape.

**Alignment requirement:** use a dedicated documents-event serializer. Do not
reuse GET or accepted serializers.

### 5. Nested metadata is not AWS-shaped

AWS metadata mirrors the state object's property tree and places a `timestamp`
at attribute leaves. RTK serializes an object property as:

```json
{
  "lights": {
    "timestamp": 1778397600,
    "children": {
      "color": {
        "timestamp": 1778397600
      }
    }
  }
}
```

The `children` wrapper and parent-object timestamp are RTK-specific. AWS clients
expect the nested property names directly under `lights`.

**Alignment requirement:** change the public serializer and, preferably, the
metadata domain model so metadata structurally mirrors state. Add nested-object
and nested-delta golden tests.

### 6. Empty sections are represented differently

AWS permits `desired` and `reported` to be omitted when absent and removes a
section when it is set to `null`. RTK stores an empty object and always returns:

```json
"state": {"desired": {}, "reported": {}, "delta": {}}
```

The underlying intent is close, but the observable JSON is different.

**Alignment requirement:** distinguish absent from empty in response
serialization, or normalize empty sections to omission in AWS-compatible mode.

### 7. Delete and recreate behavior differs

AWS DELETE has no HTTP body. On MQTT the delete payload content is ignored.
Deleting does not immediately reset the version: recreation within 48 hours
continues the version; recreation after 48 hours starts again at version 0.

RTK accepts `version` and `clientToken` in HTTP and MQTT delete bodies, rejects a
stale delete version, increments the version, writes a tombstone, and preserves
that boundary indefinitely. Its delete response is also serialized as a normal
full document with empty state and RTK extensions.

The tombstone retention may be desirable for unbind/resale audit requirements,
but it is not AWS semantics.

**Alignment requirement:** separate public shadow deletion from the private
lifecycle/audit tombstone. AWS-compatible DELETE must ignore an MQTT body and
accept no HTTP body. Decide explicitly whether the 48-hour reset is required;
if exact compatibility is the goal, store a deletion expiry and reset the
public version after that window while retaining a separate audit record.

### 8. Creation and device lifecycle differ

AWS thing registration does not create a shadow. A GET before the first update
returns not found, and the first update creates the shadow.

RTK activation/provision bootstraps an unnamed shadow with lifecycle-derived
reported fields. This means a newly provisioned RTK device has a readable
shadow and a nonzero version before any explicit shadow update.

**Alignment requirement:** choose one of these policies:

- Exact AWS mode: do not bootstrap a public shadow.
- RTK extension mode: retain bootstrap but document that initial existence,
  reported state, metadata, and version are not AWS-compatible.

### 9. Validation behavior and limits differ

AWS documents these relevant constraints:

- client token: at most 64 bytes;
- thing name: 1-128 characters matching `[a-zA-Z0-9:_-]+`;
- shadow name: 1-64 characters matching `[$a-zA-Z0-9:_-]+`;
- state document: 8 KiB for desired and reported data, excluding metadata;
- bounded JSON nesting (the current quota page states eight desired/reported
  levels; the error guide contains older wording of six, so conformance tests
  should use the quota value and verify AWS behavior before release);
- arrays cannot contain `null`;
- UTF-8 document encoding;
- documented 400, 401/403, 404, 409, 413, 415, 429, 500, and 503 cases.

RTK trims names and tokens but does not enforce these AWS grammars or sizes. Go
JSON decoding accepts arrays containing `null`, unknown top-level fields are
ignored, and no shadow-specific request/document throttling is visible in the
reviewed path.

**Alignment requirement:** implement one shared validator used by HTTP and
MQTT before mutation. Count UTF-8 bytes, validate the resulting stored state
(not only the request patch), and return the AWS status and error payload.

### 10. Named-shadow list semantics differ

AWS `ListNamedShadowsForThing` accepts `pageSize` from 1 to 100 and
`nextToken`, and returns `results`, optional `nextToken`, and an epoch
`timestamp`. The unnamed shadow is excluded. AWS documents an empty result when
the thing does not exist.

RTK returns all sorted names as `{"results": [...]}`, with no pagination or
timestamp, and first requires the device to exist. It therefore returns 404 for
a missing device instead of AWS's documented empty list behavior.

**Alignment requirement:** implement the AWS route, opaque stable pagination,
timestamp, argument validation, and missing-thing behavior. Keep the current
route as a convenience alias if needed.

### 11. HTTP authentication and authorization differ

AWS data-plane HTTP supports SigV4/IAM credentials or mutual TLS client
certificates and authorizes Get, Update, Delete, and List actions on a thing
resource. AWS treats app-writes-desired/device-writes-reported as the usual
application pattern, not as an intrinsic payload-section permission rule.

RTK HTTP uses bearer scopes and forbids device/camera tokens from including
`desired`. That restriction is a valid product policy, but not AWS behavior.
Also, the current MQTT handler receives the shared update topic and payload,
while topic ACLs can authorize only the whole update topic. A topic ACL alone
cannot allow `reported` but reject `desired` on that same topic.

**Alignment requirement:** keep product authorization outside the AWS document
semantic. If section-level MQTT enforcement is required, propagate authenticated
publisher claims to the handler or use a broker payload-aware authorization
hook; do not claim that ordinary topic ACLs enforce it.

### 12. HTTP-to-MQTT notification failure has an ambiguous outcome

For an RTK HTTP desired update, the shadow is committed before the MQTT delta is
published. If delta publication fails, HTTP returns 503 even though the state
and version already changed. A client retry can increment the version again.

AWS-compatible clients need a clear mutation boundary: a failed response should
not silently mean “committed but notification failed.”

**Alignment requirement:** commit an outbox/event record atomically with the
shadow mutation, return the accepted shadow update once durable, and deliver
MQTT notifications asynchronously with retry and version ordering. Do not turn
a post-commit notification failure into an update failure.

## Semantics already aligned

These parts should be retained and protected with conformance tests:

- A missing shadow is created by update.
- Updates merge only supplied object fields.
- A property set to `null` is deleted.
- `desired: null` and `reported: null` clear their respective sections.
- Arrays are atomic replacement values.
- Delta includes desired-only and desired-different fields, excludes
  reported-only fields, and preserves nested paths.
- Omitting a request version bypasses optimistic version matching.
- A matching reported value clears the corresponding delta.
- Named and unnamed shadows use independent state documents.
- MQTT action and response suffixes match AWS.
- MQTT request failures publish a structured rejected message with code,
  message, timestamp, and optional client token.

## Recommended alignment plan

### P0: semantic correctness

1. Implement atomic Redis mutation/CAS and return committed previous/current
   documents.
2. Add an atomic outbox or equivalent reliable ordered notification mechanism.
3. Add race tests with simultaneous versioned and unversioned writers.

### P1: AWS document conformance

1. Split serializers for GET accepted, UPDATE accepted, DELETE accepted,
   documents, delta, list, and errors.
2. Fix nested metadata and omission of absent sections.
3. Add the shared AWS validator and exact error mapping.
4. Define delete/recreate and bootstrap compatibility mode explicitly.

### P2: AWS wire compatibility

1. Add `$aws/things/{thingName}/shadow...` topic aliases.
2. Add `/things/{thingName}/shadow` and
   `ListNamedShadowsForThing` HTTP aliases.
3. Add pagination and AWS list response shape.
4. Provide a documented identity mapping from AWS `thingName` to RTK `devid`.

### P3: authentication and SDK verification

1. Decide whether compatibility means payload/topic compatibility only, or
   also SigV4 and mTLS data-plane compatibility.
2. Run AWS SDK and embedded Device SDK conformance tests against RTK.
3. Run the same black-box fixture suite against AWS IoT Core and RTK and compare
   normalized results.

## Minimum conformance test matrix

The release gate should execute each case through both HTTP and MQTT where the
operation exists:

1. GET before creation.
2. First desired update and first reported update.
3. Partial nested merge.
4. Leaf deletion, section deletion, and empty document.
5. Nested delta and delta clearing.
6. Whole-array replacement and rejection of an array containing `null`.
7. Matching version, stale version, and omitted version.
8. Two simultaneous writers using the same version; exactly one may succeed.
9. Two simultaneous unversioned patches; both fields must survive in a
   serialized final state with distinct versions.
10. UPDATE accepted shape versus GET accepted shape.
11. Exact `documents` and `delta` event shapes.
12. Delete response, immediate recreate, and recreate after the configured
    compatibility window.
13. Named and unnamed isolation; list pagination and missing thing.
14. 64-byte and 65-byte client tokens.
15. Valid and invalid thing/shadow names.
16. Maximum state size, excessive nesting, invalid UTF-8, and unsupported media
    type.
17. HTTP commit while MQTT delivery is temporarily unavailable.
18. Device/app authorization cases, tested separately from AWS payload
    semantics.

## Final recommendation

Do not advertise the current API as “AWS-compatible.” A precise description is
“AWS IoT Shadow-inspired.” The shortest safe path to alignment is:

1. fix atomic mutation and notification durability;
2. make response documents and validation AWS-conformant;
3. add AWS route/topic aliases;
4. retain RTK lifecycle and authorization rules only as explicit extensions;
5. prove compatibility with a shared black-box suite against AWS and RTK.
