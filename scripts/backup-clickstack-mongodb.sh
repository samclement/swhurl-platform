#!/usr/bin/env bash
# Dump the ClickStack (HyperDX) MongoDB database into an age-encrypted archive.
# Plaintext only ever exists in the pipe between mongodump and age.
set +x
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NAMESPACE="${MONGO_NAMESPACE:-observability}"
WORKLOAD="${MONGO_WORKLOAD:-deploy/clickstack-mongodb}"
DATABASE="${MONGO_DATABASE:-hyperdx}"
BACKUP_DIR="${BACKUP_DIR:-$HOME/.local/state/swhurl-platform/backups}"
DRY_RUN="${DRY_RUN:-false}"

recipient="${AGE_RECIPIENT:-$(sed -n 's/^[[:space:]]*age:[[:space:]]*\(age1[0-9a-z]*\).*/\1/p' "$ROOT/.sops.yaml" | head -n 1)}"
[[ -n "$recipient" ]] || { echo "[ERROR] No age recipient found in .sops.yaml (set AGE_RECIPIENT)" >&2; exit 1; }

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
archive="$BACKUP_DIR/clickstack-mongodb-$stamp.archive.gz.age"

if [[ "$DRY_RUN" == "true" ]]; then
  echo "Plan (backup-clickstack-mongodb):"
  echo "  - mongodump --db $DATABASE from $NAMESPACE/$WORKLOAD"
  echo "  - encrypt to age recipient $recipient"
  echo "  - write $archive and a metadata file; no cluster changes"
  exit 0
fi

for cmd in kubectl age sha256sum; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "[ERROR] Missing required command: $cmd" >&2; exit 1; }
done

umask 077
mkdir -p "$BACKUP_DIR"
partial="$archive.partial"
trap 'rm -f -- "$partial"' EXIT

kubectl -n "$NAMESPACE" exec "$WORKLOAD" -- \
  mongodump --quiet --db "$DATABASE" --archive --gzip \
  | age -r "$recipient" -o "$partial"
[[ -s "$partial" ]] || { echo "[ERROR] Backup archive is empty" >&2; exit 1; }

counts="$(kubectl -n "$NAMESPACE" exec "$WORKLOAD" -- mongosh "$DATABASE" --quiet --eval \
  'const c = {}; db.getCollectionNames().sort().forEach(n => { c[n] = db[n].countDocuments(); }); print(JSON.stringify(c));')"
version="$(kubectl -n "$NAMESPACE" exec "$WORKLOAD" -- mongod --version | sed -n 's/^db version v//p')"

mv -- "$partial" "$archive"
trap - EXIT
sha="$(sha256sum "$archive" | cut -d' ' -f1)"
cat > "${archive%.archive.gz.age}.json" <<EOF
{"created": "$stamp", "source": "$NAMESPACE/$WORKLOAD", "database": "$DATABASE", "mongodb_version": "$version", "age_recipient": "$recipient", "sha256": "$sha", "collections": $counts}
EOF

echo "[OK] Encrypted backup: $archive"
echo "[OK] Metadata: ${archive%.archive.gz.age}.json"
echo "[INFO] Copy both files off-host; restore needs the age private key (see docs/runbook.md#recovery)."
