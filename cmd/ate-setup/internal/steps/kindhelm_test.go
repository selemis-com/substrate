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
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"go.ate.dev/substrate/cmd/ate-setup/internal/kube"
)

func TestWriteKindHelmChart(t *testing.T) {
	objects, err := kube.DecodeManifestBytes([]byte(`
apiVersion: v1
kind: Namespace
metadata:
  name: podcertificate-controller-system
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: snapshots
  annotations:
    example.com/owner: substrate
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: envoy-config
data:
  example: '{{ downstream expression }}'
`))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeKindHelmChart(dir, "substrate", "v1.0.0-rc.3", objects); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "substrate", "manifests.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := kube.DecodeManifestBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].GetKind() != "PersistentVolumeClaim" || got[1].GetKind() != "ConfigMap" {
		t.Fatalf("unexpected chart objects: %v", got)
	}
	annotations := got[0].GetAnnotations()
	if annotations["helm.sh/resource-policy"] != "keep" || annotations["example.com/owner"] != "substrate" {
		t.Errorf("unexpected PVC annotations: %v", annotations)
	}
	value, _, err := unstructured.NestedString(got[1].Object, "data", "example")
	if err != nil || value != "{{ downstream expression }}" {
		t.Errorf("unexpected ConfigMap value %q: %v", value, err)
	}
}
