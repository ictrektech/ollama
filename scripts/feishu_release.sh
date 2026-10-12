#!/usr/bin/env bash
# Feishu column/date-row helpers migrated from ollama_server/scripts/build_image.sh.
read_feishu_field() {
  local field="$1"
  python3 - "$FEISHU_CONFIG_FILE" "$field" <<'PY'
import json, sys
path, field = sys.argv[1], sys.argv[2]
with open(path, 'r', encoding='utf-8') as f:
    data = json.load(f)
val = data.get(field, "")
if not isinstance(val, str):
    val = str(val)
print(val)
PY
}

json_extract_or_fail() {
  local resp="$1"
  local py="$2"
  python3 - "$resp" "$py" <<'PY'
import json, sys
resp = sys.argv[1]
code = sys.argv[2]
if not resp:
    raise SystemExit("empty response")
try:
    data = json.loads(resp)
except Exception as e:
    raise SystemExit(f"invalid json response: {resp[:500]!r}, error={e}")
ns = {"data": data}
exec(code, ns, ns)
PY
}

get_feishu_token() {
  local app_id="$1"
  local app_secret="$2"
  local resp

  resp=$(
    curl --fail -sS -X POST 'https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal' \
      -H 'Content-Type: application/json' \
      -d "{
        \"app_id\": \"${app_id}\",
        \"app_secret\": \"${app_secret}\"
      }"
  ) || {
    err "get_feishu_token: curl failed"
    return 1
  }

  python3 - "$resp" <<'PY'
import json, sys
resp = sys.argv[1]
if not resp:
    raise SystemExit("get_feishu_token: empty response")
try:
    data = json.loads(resp)
except Exception as e:
    raise SystemExit(f"get_feishu_token: invalid json: {resp[:500]!r}, error={e}")
if data.get("code") != 0:
    raise SystemExit(f"get_feishu_token failed: {data}")
print(data["tenant_access_token"])
PY
}

feishu_api_json() {
  local method="$1"
  local url="$2"
  local token="$3"
  local body="${4:-}"

  if [[ -n "$body" ]]; then
    curl --fail -sS -X "$method" "$url" \
      -H "Authorization: Bearer ${token}" \
      -H "Content-Type: application/json" \
      --data "$body"
  else
    curl --fail -sS -X "$method" "$url" \
      -H "Authorization: Bearer ${token}"
  fi
}

get_sheet_id_by_title() {
  local token="$1"
  local spreadsheet_token="$2"
  local target_title="$3"
  local resp

  resp=$(
    feishu_api_json "GET" \
      "https://open.feishu.cn/open-apis/sheets/v3/spreadsheets/${spreadsheet_token}/sheets/query" \
      "$token"
  ) || {
    err "get_sheet_id_by_title: curl failed"
    return 1
  }

  python3 - "$target_title" "$resp" <<'PY'
import sys, json
target = sys.argv[1]
resp = sys.argv[2]
if not resp:
    raise SystemExit("get_sheet_id_by_title: empty response")
try:
    data = json.loads(resp)
except Exception as e:
    raise SystemExit(f"get_sheet_id_by_title invalid json: {resp[:500]!r}, error={e}")
if data.get("code") != 0:
    raise SystemExit(f"query sheets failed: {data}")
for s in data["data"]["sheets"]:
    if s.get("title") == target:
        print(s["sheet_id"])
        raise SystemExit(0)
raise SystemExit(f"sheet title not found: {target}")
PY
}

get_range_values() {
  local token="$1"
  local spreadsheet_token="$2"
  local range="$3"

  feishu_api_json "GET" \
    "https://open.feishu.cn/open-apis/sheets/v2/spreadsheets/${spreadsheet_token}/values/${range}" \
    "$token"
}

find_component_column_letter() {
  local token="$1"
  local spreadsheet_token="$2"
  local sheet_id="$3"
  local component_name="$4"
  local resp

  resp=$(get_range_values "$token" "$spreadsheet_token" "${sheet_id}!A1:AZ1") || {
    err "find_component_column_letter: read range failed"
    return 1
  }

  python3 - "$component_name" "$resp" <<'PY'
import sys, json
target = sys.argv[1]
resp = sys.argv[2]
if not resp:
    raise SystemExit("find_component_column_letter: empty response")
try:
    data = json.loads(resp)
except Exception as e:
    raise SystemExit(f"find_component_column_letter invalid json: {resp[:500]!r}, error={e}")
if data.get("code") != 0:
    raise SystemExit(f"read header failed: {data}")
values = data.get("data", {}).get("valueRange", {}).get("values", [])
row = values[0] if values else []
for i, v in enumerate(row, start=1):
    if str(v).strip() == target:
        n = i
        s = ""
        while n > 0:
            n, r = divmod(n - 1, 26)
            s = chr(ord("A") + r) + s
        print(s)
        raise SystemExit(0)
raise SystemExit(f"component column not found in row1: {target}")
PY
}

find_or_create_component_column_letter() {
  local token="$1"
  local spreadsheet_token="$2"
  local sheet_id="$3"
  local component_name="$4"
  local image_repo="$5"
  local resp

  resp=$(get_range_values "$token" "$spreadsheet_token" "${sheet_id}!A1:ZZ2") || {
    err "find_or_create_component_column_letter: read range failed"
    return 1
  }

  local resolved
  resolved="$(python3 - "$component_name" "$resp" <<'PY'
import sys, json

target = sys.argv[1]
resp = sys.argv[2]

def letters(n):
    s = ""
    while n > 0:
        n, r = divmod(n - 1, 26)
        s = chr(ord("A") + r) + s
    return s

if not resp:
    raise SystemExit("find_or_create_component_column_letter: empty response")
try:
    data = json.loads(resp)
except Exception as e:
    raise SystemExit(f"find_or_create_component_column_letter invalid json: {resp[:500]!r}, error={e}")
if data.get("code") != 0:
    raise SystemExit(f"read header failed: {data}")

def cell_text(value):
    if value is None:
        return ""
    return str(value).strip()

values = data.get("data", {}).get("valueRange", {}).get("values", [])
header = values[0] if values else []
last_used = 1
for i, value in enumerate(header, start=1):
    text = cell_text(value)
    if text:
        last_used = i
    if text == target:
        print(f"exists {letters(i)}")
        raise SystemExit(0)

print(f"create {letters(last_used + 1)}")
PY
)"

  local action col
  action="${resolved%% *}"
  col="${resolved#* }"
  if [[ "$action" == "create" ]]; then
    log "Component column ${component_name} not found; creating ${col}" >&2
    write_cell "$token" "$spreadsheet_token" "$sheet_id" "${col}1" "$component_name" >/dev/null
    write_cell "$token" "$spreadsheet_token" "$sheet_id" "${col}2" "$image_repo" >/dev/null
  fi
  echo "$col"
}

find_date_row() {
  local token="$1"
  local spreadsheet_token="$2"
  local sheet_id="$3"
  local target_date="$4"
  local resp

  resp=$(get_range_values "$token" "$spreadsheet_token" "${sheet_id}!A4:A2000") || {
    err "find_date_row: read range failed"
    return 1
  }

  python3 - "$target_date" "$resp" <<'PY'
import sys, json
target = sys.argv[1]
resp = sys.argv[2]
if not resp:
    raise SystemExit("find_date_row: empty response")
try:
    data = json.loads(resp)
except Exception as e:
    raise SystemExit(f"find_date_row invalid json: {resp[:500]!r}, error={e}")
if data.get("code") != 0:
    raise SystemExit(f"read date column failed: {data}")
values = data.get("data", {}).get("valueRange", {}).get("values", [])
for idx, row in enumerate(values, start=4):
    if row and str(row[0]).strip() == target:
        print(idx)
        raise SystemExit(0)
print("")
PY
}

prepend_date_row() {
  local token="$1"
  local spreadsheet_token="$2"
  local sheet_id="$3"
  local today="$4"
  local resp

  resp=$(
    feishu_api_json "POST" \
      "https://open.feishu.cn/open-apis/sheets/v2/spreadsheets/${spreadsheet_token}/values_prepend" \
      "$token" \
      "{\"valueRange\":{\"range\":\"${sheet_id}!A4:A4\",\"values\":[[\"${today}\"]]}}"
  ) || {
    err "prepend_date_row: curl failed"
    return 1
  }

  python3 - "$resp" <<'PY'
import json, sys
resp = sys.argv[1]
if not resp:
    raise SystemExit("prepend_date_row: empty response")
try:
    data = json.loads(resp)
except Exception as e:
    raise SystemExit(f"prepend_date_row invalid json: {resp[:500]!r}, error={e}")
if data.get("code") != 0:
    raise SystemExit(f"prepend_date_row failed: {data}")
print("ok")
PY
}

write_cell() {
  local token="$1"
  local spreadsheet_token="$2"
  local sheet_id="$3"
  local cell="$4"
  local value="$5"
  local resp

  resp=$(
    feishu_api_json "PUT" \
      "https://open.feishu.cn/open-apis/sheets/v2/spreadsheets/${spreadsheet_token}/values" \
      "$token" \
      "{\"valueRange\":{\"range\":\"${sheet_id}!${cell}:${cell}\",\"values\":[[\"${value}\"]]}}"
  ) || {
    err "write_cell: curl failed"
    return 1
  }

  python3 - "$resp" <<'PY'
import json, sys
resp = sys.argv[1]
if not resp:
    raise SystemExit("write_cell: empty response")
try:
    data = json.loads(resp)
except Exception as e:
    raise SystemExit(f"write_cell invalid json: {resp[:500]!r}, error={e}")
if data.get("code") != 0:
    raise SystemExit(f"write_cell failed: {data}")
print("ok")
PY
}

