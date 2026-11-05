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

package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:         "package-kind-helm DIRECTORY",
		Short:       "Generate Helm charts for the Kind install from release images",
		Annotations: map[string]string{clusterAnnotation: "no"},
		Args:        cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return env.PackageKindHelm(cmd.Context(), args[0])
		},
	})

	createCmd.AddCommand(&cobra.Command{
		Use:   "kind-helm-prerequisites HASH_FILE",
		Short: "Prepare signing authorities and API configuration for the Kind Helm install",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hash, err := env.PrepareKindHelm(cmd.Context())
			if err != nil {
				return err
			}
			return os.WriteFile(args[0], []byte(hash+"\n"), 0o600)
		},
	})

	createCmd.AddCommand(&cobra.Command{
		Use:   "kind-helm-postgres-roles",
		Short: "Initialize the bundled PostgreSQL roles after the storage chart starts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return env.SetupKindHelmPostgres(cmd.Context())
		},
	})
}
