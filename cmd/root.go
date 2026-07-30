/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"flag"
	"os"

	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/opendatahub-io/model-registry-operator/internal/setup"
)

var (
	scheme   = setup.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

// rootCmd is the base command. When invoked without a subcommand it runs the
// modelregistry behavior so that existing Deployments using the current image
// continue to work without modification.
var rootCmd = &cobra.Command{
	Use:   "manager",
	Short: "Model Registry Operator",
	Long: `The model-registry-operator binary hosts multiple operators that run as
independent processes from the same container image, selected via subcommand.

Running the binary without a subcommand defaults to the "modelregistry"
behavior for backward compatibility.`,
	// Default to the modelregistry behavior when no subcommand is given.
	RunE:          runModelRegistry,
	SilenceUsage:  true,
	SilenceErrors: false,
}

// Execute adds all child commands to the root command and runs it.
// This is called by main.main(). It only needs to happen once.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	// Register modelregistry flags on the standard flag set (including zap
	// options) and expose them as persistent flags on the root command so they
	// apply both to the bare invocation and the "modelregistry" subcommand.
	bindModelRegistryFlags(flag.CommandLine)
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)

	rootCmd.AddCommand(modelRegistryCmd)
	rootCmd.AddCommand(catalogCmd)
	rootCmd.AddCommand(aihubCmd)
}

// zapOpts holds the zap logger options bound to command-line flags.
var zapOpts = zap.Options{
	Development: true,
}
