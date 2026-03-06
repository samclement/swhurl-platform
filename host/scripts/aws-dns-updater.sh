#!/usr/bin/env bash
set -Eeuo pipefail

# Update Route53 A records for homelab hosts.
# Usage: aws-dns-updater.sh
# - Accepts optional overrides via env:
#     AWS_PROFILE   (default: default)
#     AWS_ZONE_ID   (defaults to swhurl.com hosted zone)
#     BASE_RECORD   (default: homelab.swhurl.com)
#     WILDCARD_RECORD (default: *.homelab.swhurl.com)
#     DYNAMIC_DNS_RECORDS (comma-separated FQDNs; defaults to BASE_RECORD,WILDCARD_RECORD)

# Defaults specific to swhurl.com (override via env for other zones)
DEF_ZONE_ID="${DEF_ZONE_ID:-Z08316812BZVAZ9D79ZRO}"
BASE_RECORD="${BASE_RECORD:-homelab.swhurl.com}"
WILDCARD_RECORD="${WILDCARD_RECORD:-*.homelab.swhurl.com}"
DYNAMIC_DNS_RECORDS="${DYNAMIC_DNS_RECORDS:-$BASE_RECORD,$WILDCARD_RECORD}"
RECORDS=()

ZONE_ID="${AWS_ZONE_ID:-$DEF_ZONE_ID}"

AWS_PROFILE="${AWS_PROFILE:-default}"
export AWS_PROFILE

if [[ "$#" -ne 0 ]]; then
  echo "Usage: $0" >&2
  exit 1
fi

NEW_IP="$(curl -s checkip.amazonaws.com || true)"
if [[ ! $NEW_IP =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
  echo "Could not get current IP address: $NEW_IP" >&2
  exit 1
fi
echo "New IP - $NEW_IP"

export PATH=$PATH:/usr/local/bin

trim_whitespace() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

load_records() {
  local entry normalized
  local -a raw_records
  IFS=',' read -r -a raw_records <<< "$DYNAMIC_DNS_RECORDS"

  declare -A seen=()
  for entry in "${raw_records[@]}"; do
    normalized="$(trim_whitespace "$entry")"
    normalized="${normalized%.}"
    [[ -n "$normalized" ]] || continue
    if [[ ! "$normalized" =~ ^[A-Za-z0-9*.-]+$ ]]; then
      echo "Invalid record name in DYNAMIC_DNS_RECORDS: $normalized" >&2
      exit 1
    fi
    if [[ -n "${seen[$normalized]:-}" ]]; then
      continue
    fi
    seen["$normalized"]=1
    RECORDS+=("$normalized")
  done

  if [[ "${#RECORDS[@]}" -eq 0 ]]; then
    echo "No valid records resolved from DYNAMIC_DNS_RECORDS" >&2
    exit 1
  fi
}

record_ip() {
  local record="$1"
  local query_record="${record}."
  local ip
  ip="$(
    aws route53 list-resource-record-sets \
      --hosted-zone-id "$ZONE_ID" \
      --query "ResourceRecordSets[?Name == '${query_record}' && Type == 'A'] | [0].ResourceRecords[0].Value" \
      --output text 2>/dev/null || true
  )"
  if [[ "$ip" == "None" || ! $ip =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
    printf ''
    return 0
  fi
  printf '%s' "$ip"
}

load_records

declare -A OLD_IPS=()
all_unchanged=1
for record in "${RECORDS[@]}"; do
  old_ip="$(record_ip "$record")"
  OLD_IPS["$record"]="$old_ip"
  if [[ "$NEW_IP" != "$old_ip" ]]; then
    all_unchanged=0
  fi
done

if (( all_unchanged == 1 )); then
  echo "IP unchanged for records: ${RECORDS[*]} ($NEW_IP)"
  exit 0
fi

TMP_FILE="$(mktemp /tmp/dynamic-dns.XXXXXXXX)"
trap 'rm -f "$TMP_FILE"' EXIT
{
  printf '{\n'
  printf '  "Comment": "Auto updating @ %s",\n' "$(date)"
  printf '  "Changes": [\n'
  for idx in "${!RECORDS[@]}"; do
    record="${RECORDS[$idx]}"
    printf '    {\n'
    printf '      "Action": "UPSERT",\n'
    printf '      "ResourceRecordSet": {\n'
    printf '        "ResourceRecords": [{ "Value": "%s" }],\n' "$NEW_IP"
    printf '        "Name": "%s",\n' "$record"
    printf '        "Type": "A",\n'
    printf '        "TTL": 300\n'
    printf '      }\n'
    if (( idx == ${#RECORDS[@]} - 1 )); then
      printf '    }\n'
    else
      printf '    },\n'
    fi
  done
  printf '  ]\n'
  printf '}\n'
} >"$TMP_FILE"

for record in "${RECORDS[@]}"; do
  echo "Updating $record from ${OLD_IPS[$record]:-<none>} to $NEW_IP"
done
aws route53 change-resource-record-sets --hosted-zone-id "$ZONE_ID" --change-batch "file://$TMP_FILE"
