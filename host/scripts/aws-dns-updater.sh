#!/usr/bin/env bash
set -Eeuo pipefail

# Update wildcard A record in Route53 for *.homelab.swhurl.com.
# Usage: aws-dns-updater.sh
# - Accepts optional overrides via env:
#     AWS_PROFILE   (default: default)
#     AWS_ZONE_ID   (defaults to swhurl.com hosted zone)

# Defaults specific to swhurl.com (override via env for other zones)
DEF_ZONE_ID="${DEF_ZONE_ID:-Z08316812BZVAZ9D79ZRO}"
WILDCARD_RECORD="*.homelab.swhurl.com"

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

QUERY_RECORD="${WILDCARD_RECORD}."
OLD_IP="$(
  aws route53 list-resource-record-sets \
    --hosted-zone-id "$ZONE_ID" \
    --query "ResourceRecordSets[?Name == '${QUERY_RECORD}' && Type == 'A'] | [0].ResourceRecords[0].Value" \
    --output text 2>/dev/null || true
)"
if [[ "$OLD_IP" == "None" || ! $OLD_IP =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
  OLD_IP=""
fi

if [[ "$NEW_IP" == "$OLD_IP" ]]; then
  echo "IP unchanged for $WILDCARD_RECORD ($OLD_IP)"
  exit 0
fi

TMP_FILE="$(mktemp /tmp/dynamic-dns.XXXXXXXX)"
cat >"$TMP_FILE" <<EOF
{
  "Comment": "Auto updating @ $(date)",
  "Changes": [{
    "Action": "UPSERT",
    "ResourceRecordSet": {
      "ResourceRecords": [{ "Value": "$NEW_IP" }],
      "Name": "$WILDCARD_RECORD",
      "Type": "A",
      "TTL": 300
    }
  }]
}
EOF

echo "Updating $WILDCARD_RECORD from ${OLD_IP:-<none>} to $NEW_IP"
aws route53 change-resource-record-sets --hosted-zone-id "$ZONE_ID" --change-batch "file://$TMP_FILE"
rm -f "$TMP_FILE"
