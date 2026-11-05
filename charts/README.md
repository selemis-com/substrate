# Kind Helm install

The Kind charts package the upstream manifests and release images. Use a
checkout of the upstream release that matches the image tag. The charts
target the canonical namespaces and use gVisor, Envoy, the bundled PostgreSQL
database, and the RustFS snapshot bucket.

Use a clean cluster for the first Helm install. Create it with the command
below, or use a Kind cluster with the same certificate projection features
and local registry wiring.

```sh
KIND_CLUSTER_NAME=substrate-dev hack/create-kind-cluster.sh
hack/install-kind-helm.sh kind-substrate-dev v1.0.0-rc.3 \
  spiffe://cluster.local/ns/client-system/sa/actor-client
```

The final argument is a comma-separated router allowlist. An empty list
denies actor ingress. Use the SPIFFE ID of the ServiceAccount in your client
deployment. Configure API authentication and atespace access separately using
the [authorization guide](../docs/guides/authorization.md). The Kind profile
uses the shared `ate-client` ServiceAccount for bootstrap administration.

The installer creates the signing pools and API settings, then installs the
CRDs, certificate controller, storage, atelet, and control plane. Repeating
the command for the same release preserves signing keys and data. Use a
fresh Kind cluster when changing Substrate release candidates.

The caller supplies the `WorkerPool` and actor templates. The installer prints
the gVisor worker image to use with `gvisor-default`.

## Build the chart directories

```sh
go run ./cmd/ate-setup --kind \
  --image-repo ghcr.io/agent-substrate/substrate --image-tag v1.0.0-rc.3 \
  package-kind-helm ./kind-charts
```

The output directory must be new. Each child chart can be inspected with
`helm template` or archived with `helm package`. The image digests, namespaces,
and runtime assets are resolved at generation time. Set
`ingressAllowedSPIFFEIDs` to a comma-separated string when installing the
`substrate` chart. Its empty default denies actor ingress. The same checkout and
configuration must supply the bootstrap steps. Pass the hash written by
`ate-setup create kind-helm-prerequisites HASH_FILE` to the `substrate` chart as
`--set-string apiEnvHash=HASH` so configuration changes roll the API server.

The [Helm Kind workflow](../.github/workflows/helm-kind.yaml) checks that a second
install preserves signing Secrets and restores an Actor's durable volume
from object storage.

A passing workflow uploads `substrate-kind-charts.zip`. It contains the five
generated charts from the first install and the matching `worker-image.txt`.
The smoke test supplies its own router identity at install time.
