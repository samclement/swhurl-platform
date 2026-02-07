#!/usr/bin/env bash
set -Eeuo pipefail

# Update one or more A records in Route53 for <subdomain>.swhurl.com
# Usage: aws-dns-updater.sh <subdomain> [<subdomain> ...]
# - Looks up current external IP once and UPSERTs each hostname
# - Accepts optional overrides via env:
#     AWS_PROFILE   (default: default)
#     AWS_ZONE_ID   (defaults to swhurl.com hosted zone)
#     AWS_NAMESERVER (defaults to an AWS authoritative NS for swhurl.com)

# Defaults specific to swhurl.com (override via env for other zones)
DEF_ZONE_ID="${DEF_ZONE_ID:-Z08316812BZVAZ9D79ZRO}"
DEF_NAMESERVER="${DEF_NAMESERVER:-ns-758.awsdns-30.net}"

ZONE_ID="${AWS_ZONE_ID:-$DEF_ZONE_ID}"
NAMESERVER="${AWS_NAMESERVER:-$DEF_NAMESERVER}"

AWS_PROFILE="${AWS_PROFILE:-default}"
export AWS_PROFILE

if [[ "$#" -lt 1 ]]; then
  echo "Usage: $0 <subdomain> [<subdomain> ...]" >&2
  exit 1
fi

# One external IP lookup for all updates
NEW_IP="$(curl -s checkip.amazonaws.com || true)"
if [[ ! $NEW_IP =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
  echo "Could not get current IP address: $NEW_IP" >&2
  exit 1
fi
echo "New IP - $NEW_IP"

export PATH=$PATH:/usr/local/bin

for subdomain in "$@"; do
  [[ -n "$subdomain" ]] || continue
  hostname="${subdomain}.swhurl.com"

  # Best-effort old IP lookup; allow empty for first-time UPSERTs
  OLD_IP="$(dig +short "$hostname" @"$NAMESERVER" | head -n1 || true)"
  if [[ -n "$OLD_IP" && ! $OLD_IP =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]]; then
    echo "Non-A record for $hostname, proceeding with UPSERT"
    OLD_IP=""
  fi

  if [[ "$NEW_IP" == "$OLD_IP" ]]; then
    echo "IP unchanged for $hostname ($OLD_IP)"
    continue
  fi

  TMP_FILE="$(mktemp /tmp/dynamic-dns.XXXXXXXX)"
  cat >"$TMP_FILE" <<EOF
{
  "Comment": "Auto updating @ $(date)",
  "Changes": [{
    "Action": "UPSERT",
    "ResourceRecordSet": {
      "ResourceRecords": [{ "Value": "$NEW_IP" }],
      "Name": "$hostname",
      "Type": "A",
      "TTL": 300
    }
  }]
}
EOF

  echo "Updating $hostname from ${OLD_IP:-<none>} to $NEW_IP"
  aws route53 change-resource-record-sets --hosted-zone-id "$ZONE_ID" --change-batch "file://$TMP_FILE"
  rm -f "$TMP_FILE"
done

