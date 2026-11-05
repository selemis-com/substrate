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

tag="${1:?usage: smoke.sh UPSTREAM_RELEASE_TAG}"
export KIND_CLUSTER_NAME=helm-e2e
export KO_DOCKER_REPO=localhost:5001
export KO_DEFAULTPLATFORMS="linux/$(go env GOARCH)"
context="kind-$KIND_CLUSTER_NAME"
ingress_id=spiffe://cluster.local/ns/ate-system/sa/atenet-client
work="$(mktemp -d)"
proxy_pid=

cleanup() {
  local status=$?
  if [[ -n "$proxy_pid" ]]; then
    kill "$proxy_pid" 2>/dev/null || true
    wait "$proxy_pid" 2>/dev/null || true
  fi
  if [[ "$status" != 0 ]]; then
    if [[ -f "$work/proxy.log" ]]; then
      cat "$work/proxy.log"
    fi
    kubectl --context "$context" get pods,workerpools -A -o wide || true
    kubectl --context "$context" get events -A --sort-by=.metadata.creationTimestamp || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

kc() { kubectl --context "$context" "$@"; }
ate() { "$work/kubectl-ate" --context "$context" "$@"; }

signing_fingerprint() {
  kc -n ate-system get secret actor-id-jwt-pool actor-id-ca-pool egress-mitm-ca-pool -o json |
    jq -cS '.items | sort_by(.metadata.name) | map([.metadata.uid, .data])' || return
  kc -n podcertificate-controller-system get secret service-dns-ca-pool pod-identity-ca-pool postgres-ca-pool -o json |
    jq -cS '.items | sort_by(.metadata.name) | map([.metadata.uid, .data])'
}

start_proxy() {
  "$work/kubectl-ate" --context "$context" proxy actor helm-smoke 8000:80 -a ate-demo-counter >"$work/proxy.log" 2>&1 &
  proxy_pid=$!
  curl --silent --show-error --fail --max-time 15 --retry 20 --retry-all-errors \
    --retry-max-time 120 http://127.0.0.1:8000/readyz >/dev/null
  kill -0 "$proxy_pid"
}

hack/create-kind-cluster.sh
go test ./cmd/ate-setup/internal/steps -run '^TestWriteKindHelmChart$' -count=3 -shuffle=on
hack/install-kind-helm.sh "$context" "$tag" "$ingress_id" | tee "$work/install.log"
worker_image="$(sed -n 's/^gVisor WorkerPool image: //p' "$work/install.log")"
[[ "$worker_image" == *@sha256:* ]]

go build -o "$work/kubectl-ate" ./cmd/kubectl-ate
counter_image="$(hack/run-tool.sh ko build --bare ./demos/counter)"
[[ "$counter_image" == *@sha256:* ]]
sed -e "s|ko://go.ate.dev/substrate/cmd/ateworker-gvisor|$worker_image|" \
    -e 's/replicas: 3/replicas: 2/' demos/counter/counter.yaml.tmpl | kc apply -f -
kc -n ate-demo-counter wait --for=jsonpath='{.status.readyReplicas}'=2 \
  --timeout=5m workerpool/counter
ate create atespace ate-demo-counter
sed -e "s|ko://go.ate.dev/substrate/demos/counter|$counter_image|" \
    -e '/^\${.*}$/d' -e 's/SNAPSHOT_FIDELITY_MEMORY/SNAPSHOT_FIDELITY_VOLUMES/' \
    demos/counter/counter-template.yaml.tmpl | ate create actor-template -f -
ate create actor helm-smoke -a ate-demo-counter --template counter
start_proxy
curl --fail --show-error --silent --max-time 30 http://127.0.0.1:8000/ |
  grep -F 'preserved file counter: 1 |'
ate suspend actor helm-smoke -a ate-demo-counter --mode=sync
ate get actor helm-smoke -a ate-demo-counter -o json |
  jq -e '([.status.snapshots[] | select(.owner == "SNAPSHOT_OWNER_ACTOR")]
    | max_by(.generation | tonumber)
    | .durableSnapshot.state) == "SNAPSHOT_STORAGE_STATE_COMPLETED"' >/dev/null

kill "$proxy_pid"
wait "$proxy_pid" || true
proxy_pid=

before="$(signing_fingerprint | sha256sum)"
ATE_HELM_CHART_ARCHIVE= hack/install-kind-helm.sh "$context" "$tag" "$ingress_id"
after="$(signing_fingerprint | sha256sum)"
if [[ "$before" != "$after" ]]; then
  echo 'A signing Secret changed during the second install.' >&2
  exit 1
fi

start_proxy
curl --fail --show-error --silent --max-time 30 http://127.0.0.1:8000/ |
  grep -F 'preserved file counter: 2 |'
