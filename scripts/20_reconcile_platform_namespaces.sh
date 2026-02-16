#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  # This removes the Helm release record without deleting namespaces (the chart marks
  # Namespace resources with helm.sh/resource-policy=keep). scripts/99_execute_teardown.sh
  # is responsible for deleting namespaces in a deterministic, gated way.
  log_info "Destroying platform-namespaces Helm release (namespaces are deleted by scripts/99_execute_teardown.sh)"
  helmfile_cmd -l component=platform-namespaces destroy >/dev/null 2>&1 || true
  exit 0
fi

need_cmd helmfile

# Helm refuses to install a chart that renders Namespace objects if those namespaces
# already exist without Helm ownership metadata. On existing clusters, adopt them.
release="platform-namespaces"
release_ns="kube-system"
namespaces=(platform-system ingress cert-manager logging observability storage apps)
for ns in "${namespaces[@]}"; do
  adopt_helm_ownership ns "$ns" "$release" "$release_ns"
done

# Namespaces are managed declaratively via a local Helm chart so the platform can
# rely on them existing before applying secrets/config.
helmfile_cmd -l component=platform-namespaces sync
log_info "Namespaces ensured (helmfile: component=platform-namespaces)"
