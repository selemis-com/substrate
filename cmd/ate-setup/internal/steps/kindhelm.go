// Copyright 2026 The Agent Substrate Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package steps

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"go.ate.dev/substrate/cmd/ate-setup/internal/config"
	"go.ate.dev/substrate/cmd/ate-setup/internal/images"
	"go.ate.dev/substrate/cmd/ate-setup/internal/kube"
)

// Helm supplies the real hash after the bootstrap has written the API config.
const kindHelmAPIEnvHash = "ATE_HELM_API_ENV_HASH"

func (e *Env) validateKindHelm() error {
	if !e.Cfg.Kind || !e.Cfg.Images.IsPrebuilt() {
		return fmt.Errorf("Kind Helm requires --kind, --image-repo and --image-tag")
	}
	if err := images.CheckReleaseVersion(e.Cfg.Images.Tag); err != nil {
		return err
	}
	if err := e.RequireCanonicalNamespace("Kind Helm"); err != nil {
		return err
	}
	if e.Cfg.Router == config.RouterAgentgateway || e.Cfg.CredentialProviderJSON != "" ||
		e.Cfg.AdditionalEgressExtprocService != "" {
		return fmt.Errorf("Kind Helm uses Envoy without credential injection")
	}
	if e.Cfg.PostgresReadWriteConnectionString != "" || e.Cfg.PostgresOwnerConnectionString != "" ||
		e.Cfg.CloudSQL.Instance != "" || e.Cfg.SnapshotLocation != "gs://ate-snapshots/" {
		return fmt.Errorf("Kind Helm uses bundled PostgreSQL and the ate-snapshots RustFS bucket")
	}
	_, _, err := e.postgresReadWriteConnectionStrings()
	return err
}

// PackageKindHelm renders the pinned upstream install into independently
// installable charts. It also writes the matching gVisor worker image reference.
func (e *Env) PackageKindHelm(ctx context.Context, out string) error {
	if err := e.validateKindHelm(); err != nil {
		return err
	}
	if err := os.Mkdir(out, 0o755); err != nil {
		return err
	}
	charts := []struct {
		name  string
		paths []string
	}{
		{"substrate-crds", []string{"generated", "sandboxconfig-validation.yaml"}},
		{"substrate-podcert", []string{"pod-certificate-controller.yaml"}},
		{"substrate-storage", []string{"kind/postgres", "kind/rustfs.yaml"}},
		{"substrate-atelet", []string{"kind/atelet"}},
		{"substrate", []string{
			"kind/ate-api-server", "ate-idp-server.yaml", "ate-controller.yaml",
			"ate-system-networkpolicies.yaml", "sandboxconfig-gvisor.yaml",
			"kind/ate-otel-config.yaml", "kind/otel-collector.yaml", "kind/prometheus.yaml",
		}},
	}
	for _, chart := range charts {
		var objects []*unstructured.Unstructured
		for _, path := range chart.paths {
			var raw []byte
			var err error
			if path == "generated" {
				raw, err = kube.ReadPath(e.Cfg.Manifest(path))
			} else {
				raw, err = e.render(e.Cfg.Manifest(path))
			}
			if err != nil {
				return err
			}
			if path == "ate-idp-server.yaml" {
				raw, err = setIDPServerIssuer(raw, e.Cfg.ActorJWTIssuer)
				if err != nil {
					return err
				}
			}
			raw, err = e.ResolveManifestBytes(ctx, raw)
			if err != nil {
				return err
			}
			part, err := kube.DecodeManifestBytes(raw)
			if err != nil {
				return err
			}
			objects = append(objects, part...)
		}
		if chart.name == "substrate" {
			if err := stampAPIServerEnvHash(objects, kindHelmAPIEnvHash); err != nil {
				return err
			}
			if err := setAPIServerAuthzArgs(objects, e.Cfg.APIAuthzBootstrapOwners); err != nil {
				return err
			}
			if err := setAPIServerEgressGatewayArgs(objects, e.Namespace(), e.Cfg.EgressGatewayExtraSPIFFEIDs); err != nil {
				return err
			}
			router, err := e.renderAtenetRouterManifest(ctx)
			if err != nil {
				return err
			}
			egress, err := e.renderAtenetEgressManifest(ctx, nil)
			if err != nil {
				return err
			}
			network, err := kube.DecodeManifestBytes(bytes.Join([][]byte{router, egress}, []byte("\n---\n")))
			if err != nil {
				return err
			}
			objects = append(objects, network...)
		}
		if err := writeKindHelmChart(out, chart.name, e.Cfg.Images.Tag, objects); err != nil {
			return err
		}
	}
	worker, err := images.NewPrebuilt(e.Cfg.Images, images.RemoteDigest).Pin(ctx, "ateworker-gvisor")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "worker-image.txt"), []byte(worker+"\n"), 0o644)
}

// writeKindHelmChart keeps the Kubernetes objects separate from Helm templates.
// This also preserves template expressions inside ConfigMap data.
func writeKindHelmChart(out, name, tag string, objects []*unstructured.Unstructured) error {
	dir := filepath.Join(out, name)
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		return err
	}
	var manifests bytes.Buffer
	for _, obj := range objects {
		switch obj.GetKind() {
		case "Namespace":
			if obj.GetName() == NamespacePodCert {
				// The CA bootstrap creates this namespace before Helm runs.
				continue
			}
			fallthrough
		case "CustomResourceDefinition", "PersistentVolumeClaim":
			annotations := obj.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations["helm.sh/resource-policy"] = "keep"
			obj.SetAnnotations(annotations)
		}
		raw, err := yaml.Marshal(obj.Object)
		if err != nil {
			return err
		}
		manifests.WriteString("---\n")
		manifests.Write(raw)
	}
	metadata := fmt.Sprintf("apiVersion: v2\nname: %s\ntype: application\nversion: %s-helm.1\nappVersion: %q\n",
		name, strings.TrimPrefix(tag, "v"), tag)
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(metadata), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifests.yaml"), manifests.Bytes(), 0o644); err != nil {
		return err
	}
	template := "{{ .Files.Get \"manifests.yaml\" }}\n"
	if name == "substrate" {
		template = fmt.Sprintf("{{ .Files.Get \"manifests.yaml\" | replace %q (.Values.apiEnvHash | default \"unmanaged\" | quote) }}\n", kindHelmAPIEnvHash)
	}
	return os.WriteFile(filepath.Join(dir, "templates", "manifests.yaml"), []byte(template), 0o644)
}

// PrepareKindHelm reconciles the chart prerequisites and returns the API
// configuration hash for its pod template.
func (e *Env) PrepareKindHelm(ctx context.Context) (string, error) {
	if err := e.validateKindHelm(); err != nil {
		return "", err
	}
	for _, prepare := range []func(context.Context) error{
		e.EnsureAteSystemNamespace,
		e.EnsurePodCertificateCAs,
		e.EnsureAPIServerPrerequisites,
		e.EnsureEgressMITMCAPoolSecret,
	} {
		if err := prepare(ctx); err != nil {
			return "", err
		}
	}
	return e.apiServerEnvHash(ctx)
}

// SetupKindHelmPostgres initializes the database before the API server starts.
func (e *Env) SetupKindHelmPostgres(ctx context.Context) error {
	if err := e.validateKindHelm(); err != nil {
		return err
	}
	return e.waitAndSetupBundledPostgres(ctx)
}
