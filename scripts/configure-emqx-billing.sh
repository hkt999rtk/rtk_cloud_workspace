#!/usr/bin/env bash
set -euo pipefail

: "${EMQX_API_TOKEN:?set EMQX_API_TOKEN to an EMQX dashboard/API bearer token}"
: "${VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN:?set VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN}"

api_root="${EMQX_API_URL:-http://127.0.0.1:18083/api/v5}"
# The connector stores only the HTTP origin. The action owns the callback
# path; keeping the two separate avoids EMQX joining the path twice.
callback_url="${VIDEO_CLOUD_MQTT_USAGE_CALLBACK_URL:-http://video-cloud-mqttusage.video-cloud-staging-video-cloud.svc.cluster.local:19400}"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

auth_header="Authorization: Bearer ${EMQX_API_TOKEN}"
curl_api() {
	curl --connect-timeout 5 --max-time 20 --retry 3 --retry-all-errors --retry-delay 2 "$@"
}
post_json() {
	local path=$1 body=$2 response="$tmp_dir/response.json" code
	code=$(curl_api -sS -o "$response" -w '%{http_code}' -X POST \
		-H "$auth_header" -H 'content-type: application/json' \
		"${api_root}/${path}" --data-binary "@$body")
	if [[ "$code" == 2* ]]; then return 0; fi
	if [[ "$code" != 400 ]]; then jq -c . "$response" >&2 || true; return 1; fi
	return 10
}

upsert_json() {
	local collection=$1 name=$2 body=$3 response="$tmp_dir/response.json" code id
	code=$(curl_api -sS -o "$response" -w '%{http_code}' -H "$auth_header" "${api_root}/${collection}")
	if [[ "$code" == 2* ]]; then
		id=$(jq -r --arg name "$name" '(if type == "object" then .data else . end)[]? | select(.name == $name) | .id' "$response" | head -1)
		if [[ -n "$id" && "$id" != "null" ]]; then
			curl_api -sS --fail -X PUT -H "$auth_header" -H 'content-type: application/json' \
				"${api_root}/${collection}/${id}" --data-binary "$(jq -c 'del(.name,.type)' "$body")" >/dev/null
			return 0
		fi
		if jq -e --arg name "$name" '(if type == "object" then .data else . end)[]? | select(.name == $name)' "$response" >/dev/null 2>&1; then
			# This EMQX API version does not expose an id for connectors/actions;
			# update actions through their typed name, while retaining the
			# existing connector instead of creating a duplicate.
			if [[ "$collection" == "actions" ]]; then
				curl_api -sS --fail -X PUT -H "$auth_header" -H 'content-type: application/json' \
					"${api_root}/actions/http:${name}" --data-binary "$(jq -c 'del(.name,.type)' "$body")" >/dev/null
				return 0
			fi
			if [[ "$collection" == "connectors" ]]; then
				curl_api -sS --fail -X PUT -H "$auth_header" -H 'content-type: application/json' \
					"${api_root}/connectors/http:${name}" --data-binary "$(jq -c 'del(.name,.type)' "$body")" >/dev/null
				return 0
			fi
		fi
	fi
	if post_json "$collection" "$body"; then return 0; elif [[ "$?" -ne 10 ]]; then return 1; fi
	code=$(curl_api -sS -o "$response" -w '%{http_code}' -H "$auth_header" "${api_root}/${collection}")
	if [[ "$code" != 2* ]]; then jq -c . "$response" >&2 || true; return 1; fi
	id=$(jq -r --arg name "$name" '(if type == "object" then .data else . end)[]? | select(.name == $name) | .id' "$response" | head -1)
	# EMQX returns connector/action resources without an id on this API
	# version; an existing named resource is still usable and can be left
	# unchanged. Rules expose stable ids and are updated below.
	if [[ -z "$id" || "$id" == "null" ]]; then
		if [[ "$collection" == "connectors" || "$collection" == "actions" ]]; then return 0; fi
		echo "cannot resolve ${collection}/${name}" >&2
		return 1
	fi
	curl_api -sS --fail -X PUT -H "$auth_header" -H 'content-type: application/json' \
		"${api_root}/${collection}/${id}" --data-binary "$(jq -c 'del(.name,.type)' "$body")" >/dev/null
}

jq -n --arg url "$callback_url" '{type:"http",name:"billing_usage",url:$url,enable:true,pool_size:4,pool_type:"random",connect_timeout:"15s"}' > "$tmp_dir/connector.json"
# Omitting body uses EMQX's JSON encoding of the selected row. Do not splice
# unescaped topics/client IDs or binary MQTT payloads into a JSON template.
jq -n --arg token "$VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN" '{type:"http",name:"billing_usage_publish",connector:"billing_usage",enable:true,parameters:{path:"/v1/internal/mqtt/usage",method:"post",headers:{"content-type":"application/json",authorization:("Bearer "+$token)}},resource_opts:{query_mode:"sync",worker_pool_size:8}}' > "$tmp_dir/publish-action.json"
jq -n --arg token "$VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN" '{type:"http",name:"billing_usage_delivery",connector:"billing_usage",enable:true,parameters:{path:"/v1/internal/mqtt/usage",method:"post",headers:{"content-type":"application/json",authorization:("Bearer "+$token)}},resource_opts:{query_mode:"sync",worker_pool_size:8}}' > "$tmp_dir/delivery-action.json"
# callback_id belongs to this rule evaluation and remains in the buffered row
# across HTTP retries. MQTT id alone would collapse subscriber fanout deliveries.
jq -n '{name:"billing_usage_publish",enable:true,sql:"SELECT '\''message.publish'\'' as event, uuid_v4() as callback_id, id, username, clientid, topic, base64_encode(payload) as payload_b64, node, timestamp, publish_received_at FROM \"#\" WHERE username != '\''video-cloud-server'\''",actions:["http:billing_usage_publish"]}' > "$tmp_dir/publish-rule.json"
# EMQX 5.8 (the staging broker version) exposes this event through the
# backward-compatible event topic. Newer brokers may also support the
# namespaced "$events/message/delivered" spelling.
jq -n '{name:"billing_usage_delivery",enable:true,sql:"SELECT '\''message.delivered'\'' as event, uuid_v4() as callback_id, id, username, clientid, from_username, from_clientid, topic, base64_encode(payload) as payload_b64, node, timestamp, publish_received_at FROM \"$events/message_delivered\" WHERE username != '\''video-cloud-server'\''",actions:["http:billing_usage_delivery"]}' > "$tmp_dir/delivery-rule.json"

upsert_json connectors billing_usage "$tmp_dir/connector.json"
upsert_json actions billing_usage_publish "$tmp_dir/publish-action.json"
upsert_json actions billing_usage_delivery "$tmp_dir/delivery-action.json"
upsert_json rules billing_usage_publish "$tmp_dir/publish-rule.json"
upsert_json rules billing_usage_delivery "$tmp_dir/delivery-rule.json"
echo "EMQX billing connector, actions, and rules configured"
