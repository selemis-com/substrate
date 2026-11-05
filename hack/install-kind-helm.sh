#!/usr/bin/env bash

# Copyright 2026 The Agent Substrate Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

if [[ $# -lt 2 || $# -gt 3 || "$1" != kind-* ]]; then
  echo "Usage: $0 KIND_CONTEXT UPSTREAM_RELEASE_TAG [INGRESS_SPIFFE_IDS]" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
context="$1"
tag="$2"
ingress_ids="${3:-}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cd "$root"

for command in go helm kubectl; do
  command -v "$command" >/dev/null
done

kc() { kubectl --context "$context" "$@"; }

kc get --raw /apis/certificates.k8s.io/v1beta1 >/dev/null
certificate_resources="$(kc api-resources --api-group=certificates.k8s.io -o name)"
for resource in clustertrustbundles podcertificaterequests; do
  if ! grep -Fxq "$resource.certificates.k8s.io" <<<"$certificate_resources"; then
    echo "$context does not serve $resource. Create Kind with hack/create-kind-cluster.sh." >&2
    exit 1
  fi
done

go build -o "$work/ate-setup" ./cmd/ate-setup
setup=("$work/ate-setup" --kind --context "$context" --no-dev-env --no-report-defaults
  --image-repo ghcr.io/agent-substrate/substrate --image-tag "$tag"
  --ingress-allowed-spiffe-ids "$ingress_ids")

"${setup[@]}" package-kind-helm "$work/charts"
"${setup[@]}" create kind-helm-prerequisites "$work/api-env-hash"
api_env_hash="$(cat "$work/api-env-hash")"

install_chart() {
  local name="$1" namespace="$2"
  shift 2
  helm --kube-context "$context" upgrade --install "$name" "$work/charts/$name" \
    --namespace "$namespace" --wait --timeout 5m "$@"
}

install_chart substrate-crds ate-system
kc wait --for=condition=Established --timeout=1m \
  crd/workerpools.ate.dev crd/sandboxconfigs.ate.dev crd/csidriverconfigs.ate.dev

install_chart substrate-podcert podcertificate-controller-system
kc wait --for=create --timeout=5m \
  clustertrustbundle/podidentity.podcert.ate.dev:identity:primary-bundle \
  clustertrustbundle/servicedns.podcert.ate.dev:identity:primary-bundle \
  clustertrustbundle/postgres.podcert.ate.dev:identity:primary-bundle

install_chart substrate-storage ate-system --wait-for-jobs
"${setup[@]}" create kind-helm-postgres-roles
# Submit the DaemonSet first and wait for its rollout after the API is up.
helm --kube-context "$context" upgrade --install substrate-atelet "$work/charts/substrate-atelet" \
  --namespace ate-system
install_chart substrate ate-system --set-string "apiEnvHash=$api_env_hash"
kc -n ate-system rollout status daemonset/atelet --timeout=5m

printf 'gVisor WorkerPool image: %s\n' "$(cat "$work/charts/worker-image.txt")"
