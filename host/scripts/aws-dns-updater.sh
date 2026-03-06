#!/usr/bin/env bash
set -Eeuo pipefail

# Update apex + wildcard A records in Route53 for homelab.swhurl.com.
# Usage: aws-dns-updater.sh
# - Accepts optional overrides via env:
#     AWS_PROFILE   (default: default)
#     AWS_ZONE_ID   (defaults to swhurl.com hosted zone)
#     BASE_RECORD   (default: homelab.swhurl.com)
#     WILDCARD_RECORD (default: *.homelab.swhurl.com)
# TODO: Support an env-provided record list when more explicit hostnames need automatic updates.

# Defaults specific to swhurl.com (override via env for other zones)
DEF_ZONE_ID="${DEF_ZONE_ID:-Z08316812BZVAZ9D79ZRO}"
BASE_RECORD="${BASE_RECORD:-homelab.swhurl.com}"
WILDCARD_RECORD="${WILDCARD_RECORD:-*.homelab.swhurl.com}"
RECORDS=("$BASE_RECORD" "$WILDCARD_RECORD")

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
cat >"$TMP_FILE" <<EOF
{
  "Comment": "Auto updating @ $(date)",
  "Changes": [
    {
      "Action": "UPSERT",
      "ResourceRecordSet": {
        "ResourceRecords": [{ "Value": "$NEW_IP" }],
        "Name": "$BASE_RECORD",
        "Type": "A",
        "TTL": 300
      }
    },
    {
      "Action": "UPSERT",
      "ResourceRecordSet": {
        "ResourceRecords": [{ "Value": "$NEW_IP" }],
        "Name": "$WILDCARD_RECORD",
        "Type": "A",
        "TTL": 300
      }
    }
  ]
}
EOF

for record in "${RECORDS[@]}"; do
  echo "Updating $record from ${OLD_IPS[$record]:-<none>} to $NEW_IP"
done
aws route53 change-resource-record-sets --hosted-zone-id "$ZONE_ID" --change-batch "file://$TMP_FILE"
