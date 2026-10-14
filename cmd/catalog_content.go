package cmd

import (
	"errors"
	"github.com/opendatahub-io/model-registry-operator/internal/catalogcontent"
	"github.com/spf13/cobra"
)

func init() {
	validate := &cobra.Command{Use: "catalog-content-validate", Short: "Validate shipped Catalog YAML and benchmarks before startup", RunE: func(cmd *cobra.Command, _ []string) error {
		sources, _ := cmd.Flags().GetString("sources")
		benchmarks, _ := cmd.Flags().GetString("benchmarks")
		if failure := catalogcontent.Validate(sources, benchmarks); failure != nil {
			return errors.Join(failure, failure.Write("/dev/termination-log"))
		}
		return nil
	}}
	validate.Flags().String("sources", "/data/default-sources/sources.yaml", "Shipped source configuration")
	validate.Flags().String("benchmarks", "/shared-benchmark-data", "Shipped benchmark directory")
	rootCmd.AddCommand(validate)
}
