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
	"github.com/spf13/cobra"
)

// aihubCmd is a placeholder for the AI Hub operator.
var aihubCmd = &cobra.Command{
	Use:   "aihub",
	Short: "Run the AI Hub operator (not yet implemented)",
	Long: `Runs the AI Hub operator as an independent process.

This subcommand is a placeholder and does not start any controllers yet.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cmd.Println("aihub: not yet implemented")
		return nil
	},
}
