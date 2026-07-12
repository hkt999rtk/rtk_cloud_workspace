#!/usr/bin/env bash
set -euo pipefail

: "${EMQX_API_TOKEN:?set EMQX_API_TOKEN to an EMQX dashboard/API bearer token}"
: "${VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN:?set VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN}"

api_root="${EMQX_API_URL:-http://127.0.0.1:18083/api/v5}"
callback_url="${VIDEO_CLOUD_MQTT_USAGE_CALLBACK_URL:-http://video-cloud-mqttusage.video-cloud-staging-video-cloud.svc.cluster.local:19400/v1/internal/mqtt/usage}"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

auth_header="Authorization: Bearer ${EMQX_API_TOKEN}"
post_json() {
	local path=$1 body=$2 response="$tmp_dir/response.json" code
	code=$(curl -sS -o "$response" -w '%{http_code}' -X POST \
		-H "$auth_header" -H 'content-type: application/json' \
		"${api_root}/${path}" --data-binary "@$body")
	if [[ "$code" == 2* ]]; then return 0; fi
	if [[ "$code" != 400 ]]; then jq -c . "$response" >&2 || true; return 1; fi
	return 10
}

upsert_json() {
	local collection=$1 name=$2 body=$3 response="$tmp_dir/response.json" code id
	if post_json "$collection" "$body"; then return 0; elif [[ "$?" -ne 10 ]]; then return 1; fi
	code=$(curl -sS -o "$response" -w '%{http_code}' -H "$auth_header" "${api_root}/${collection}")
	if [[ "$code" != 2* ]]; then jq -c . "$response" >&2 || true; return 1; fi
	id=$(jq -r --arg name "$name" '.data[]? | select(.name == $name) | .id' "$response" | head -1)
	if [[ -z "$id" || "$id" == "null" ]]; then echo "cannot resolve ${collection}/${name}" >&2; return 1; fi
	curl -sS --fail -X PUT -H "$auth_header" -H 'content-type: application/json' \
		"${api_root}/${collection}/${id}" --data-binary "@$body" >/dev/null
}

jq -n --arg url "$callback_url" '{type:"http",name:"billing_usage",url:$url,enable:true,pool_size:4,pool_type:"random",connect_timeout:"15s"}' > "$tmp_dir/connector.json"
jq -n --arg token "$VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN" '{type:"http",name:"billing_usage_publish",connector:"billing_usage",enable:true,parameters:{path:"/v1/internal/mqtt/usage",method:"post",headers:{"content-type":"application/json",authorization:("Bearer "+$token)},body:"${.}"},resource_opts:{query_mode:"async",worker_pool_size:8}}' > "$tmp_dir/publish-action.json"
jq -n --arg token "$VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN" '{type:"http",name:"billing_usage_delivery",connector:"billing_usage",enable:true,parameters:{path:"/v1/internal/mqtt/usage",method:"post",headers:{"content-type":"application/json",authorization:("Bearer "+$token)},body:"${.}"},resource_opts:{query_mode:"async",worker_pool_size:8}}' > "$tmp_dir/delivery-action.json"
jq -n '{name:"billing_usage_publish",enable:true,sql:"SELECT '\''message.publish'\'' as event, username, clientid, topic, payload, node, timestamp, publish_received_at FROM \"$events/message_publish\" WHERE username != '\''video-cloud-server'\''",actions:["http:billing_usage_publish"]}' > "$tmp_dir/publish-rule.json"
jq -n '{name:"billing_usage_delivery",enable:true,sql:"SELECT '\''message.delivered'\'' as event, username, clientid, from_username, from_clientid, topic, payload, node, timestamp FROM \"$events/message_delivered\" WHERE username != '\''video-cloud-server'\''",actions:["http:billing_usage_delivery"]}' > "$tmp_dir/delivery-rule.json"

upsert_json connectors billing_usage "$tmp_dir/connector.json"
upsert_json actions billing_usage_publish "$tmp_dir/publish-action.json"
upsert_json actions billing_usage_delivery "$tmp_dir/delivery-action.json"
upsert_json rules billing_usage_publish "$tmp_dir/publish-rule.json"
upsert_json rules billing_usage_delivery "$tmp_dir/delivery-rule.json"
echo "EMQX billing connector, actions, and rules configured"
