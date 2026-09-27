#!/usr/bin/env bash
# Prove a ClickStack MongoDB backup restores: decrypt it into a disposable
# namespace, restore the OTel ingestion Secret from Git with the same age key,
# and check the restored data against the backup metadata and that Secret.
# Never touches live workloads; prints no key material.
set +x
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKUP_DIR="${BACKUP_DIR:-$HOME/.local/state/swhurl-platform/backups}"
AGE_KEY_FILE="${AGE_KEY_FILE:-${SOPS_AGE_KEY_FILE:-$ROOT/age.agekey}}"
NAMESPACE="${RECOVERY_NAMESPACE:-recovery-test}"
SECRET_SOURCE="$ROOT/platform-services/otel/base/secret-hyperdx.sops.yaml"
LABEL="platform.swhurl.com/recovery-test"
KEEP="${KEEP:-false}"
DRY_RUN="${DRY_RUN:-false}"

archive="${BACKUP_FILE:-$(ls -1t "$BACKUP_DIR"/clickstack-mongodb-*.archive.gz.age 2>/dev/null | head -n 1 || true)}"
metadata="${archive%.archive.gz.age}.json"

if [[ "$DRY_RUN" == "true" ]]; then
  echo "Plan (restore-test-clickstack-mongodb):"
  echo "  - create disposable namespace $NAMESPACE (label $LABEL=true) with a throwaway MongoDB pod"
  echo "  - decrypt ${archive:-<latest backup in $BACKUP_DIR>} with $AGE_KEY_FILE and mongorestore into it"
  echo "  - decrypt $SECRET_SOURCE into $NAMESPACE and compare with the restored team ingestion key"
  echo "  - compare restored collection counts with the backup metadata, then delete $NAMESPACE"
  exit 0
fi

for cmd in kubectl age sops python3 base64; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "[ERROR] Missing required command: $cmd" >&2; exit 1; }
done
[[ -n "$archive" && -f "$archive" ]] || { echo "[ERROR] No backup archive found (set BACKUP_FILE or BACKUP_DIR)" >&2; exit 1; }
[[ -f "$metadata" ]] || { echo "[ERROR] Missing backup metadata: $metadata" >&2; exit 1; }
[[ -r "$AGE_KEY_FILE" ]] || { echo "[ERROR] Cannot read age key: $AGE_KEY_FILE" >&2; exit 1; }

expected_sha="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["sha256"])' "$metadata")"
[[ "$(sha256sum "$archive" | cut -d' ' -f1)" == "$expected_sha" ]] \
  || { echo "[ERROR] Archive checksum does not match metadata" >&2; exit 1; }

if kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
  [[ "$(kubectl get namespace "$NAMESPACE" -o jsonpath="{.metadata.labels.${LABEL//./\\.}}")" == "true" ]] \
    || { echo "[ERROR] Namespace $NAMESPACE exists and is not a recovery-test namespace; refusing to use it" >&2; exit 1; }
  echo "[ERROR] Namespace $NAMESPACE already exists; delete it or set RECOVERY_NAMESPACE" >&2; exit 1
fi

cleanup() {
  if [[ "$KEEP" == "true" ]]; then
    echo "[INFO] KEEP=true: leaving namespace $NAMESPACE"
  else
    kubectl delete namespace "$NAMESPACE" --wait=false >/dev/null 2>&1 || true
    echo "[INFO] Deleted disposable namespace $NAMESPACE"
  fi
}
kubectl create namespace "$NAMESPACE" >/dev/null
trap cleanup EXIT
kubectl label namespace "$NAMESPACE" "$LABEL=true" >/dev/null

image="${MONGO_IMAGE:-mongo:$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["mongodb_version"])' "$metadata")-focal}"
kubectl -n "$NAMESPACE" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: mongodb
  labels:
    $LABEL: "true"
spec:
  automountServiceAccountToken: false
  containers:
    - name: mongodb
      image: $image
      volumeMounts:
        - name: data
          mountPath: /data/db
  volumes:
    - name: data
      emptyDir: {}
EOF
kubectl -n "$NAMESPACE" wait --for=condition=Ready pod/mongodb --timeout=300s >/dev/null
for _ in $(seq 1 30); do
  kubectl -n "$NAMESPACE" exec mongodb -- mongosh --quiet --eval 'db.runCommand({ping: 1}).ok' >/dev/null 2>&1 && break
  sleep 2
done
echo "[OK] Disposable MongoDB ($image) ready in $NAMESPACE"

age -d -i "$AGE_KEY_FILE" "$archive" \
  | kubectl -n "$NAMESPACE" exec -i mongodb -- mongorestore --quiet --archive --gzip --drop
echo "[OK] Restored $(basename "$archive")"

SOPS_AGE_KEY_FILE="$AGE_KEY_FILE" sops decrypt "$SECRET_SOURCE" \
  | python3 -c 'import sys, yaml; d = yaml.safe_load(sys.stdin); d["metadata"]["namespace"] = sys.argv[1]; yaml.safe_dump(d, sys.stdout)' "$NAMESPACE" \
  | kubectl apply -f - >/dev/null
echo "[OK] Restored hyperdx-secret from Git into $NAMESPACE"

fail=0
restored="$(kubectl -n "$NAMESPACE" exec mongodb -- mongosh hyperdx --quiet --eval \
  'const c = {}; db.getCollectionNames().sort().forEach(n => { c[n] = db[n].countDocuments(); }); print(JSON.stringify(c));')"
if python3 -c 'import json,sys; sys.exit(json.loads(sys.argv[1]) != json.load(open(sys.argv[2]))["collections"])' "$restored" "$metadata"; then
  echo "[OK] Restored collection counts match backup metadata"
else
  echo "[BAD] Restored collection counts differ from backup metadata"; fail=1
fi

team_key="$(kubectl -n "$NAMESPACE" exec mongodb -- mongosh hyperdx --quiet --eval \
  'const k = db.teams.distinct("apiKey").filter(v => typeof v === "string" && v.length > 0); if (k.length !== 1) quit(2); print(k[0]);' 2>/dev/null || true)"
secret_key="$(kubectl -n "$NAMESPACE" get secret hyperdx-secret -o jsonpath='{.data.HYPERDX_API_KEY}' | base64 --decode 2>/dev/null || true)"
if [[ -n "$team_key" && "$team_key" == "$secret_key" ]]; then
  echo "[OK] Restored team ingestion key matches the restored Git Secret"
else
  echo "[BAD] Restored team ingestion key does not match the restored Git Secret"; fail=1
fi
unset team_key secret_key

(( fail )) && exit 1
echo "Restore test passed."
