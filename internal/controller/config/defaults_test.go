package config

import (
	"os"
	"testing"
)

func TestGetStringConfigWithDefault(t *testing.T) {
	tests := []struct {
		name       string
		configName string
		want       string
	}{
		{name: "test " + GrpcImage, configName: GrpcImage, want: "success1"},
		{name: "test " + RestImage, configName: RestImage, want: "success2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv(tt.configName, tt.want)
			if got := GetStringConfigWithDefault(tt.configName, "fail"); got != tt.want {
				t.Errorf("GetStringConfigWithDefault() = %v, want %v", got, tt.want)
			}
		})
	}
}

/*func TestParseTemplates(t *testing.T) {
	tests := []struct {
		name    string
		spec    v1alpha1.ModelRegistrySpec
		want    string
		wantErr bool
	}{
		{name: "role.yaml.tmpl"},
	}

	// parse all templates
	templates, err := ParseTemplates()
	if err != nil {
		t.Errorf("ParseTemplates() error = %v", err)
	}
	reconciler := controller.ModelRegistryReconciler{
		Log:         logr.Logger{},
		Template:    templates,
		IsOpenShift: true,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := controller.ModelRegistryParams{
				Name:      "test",
				Namespace: "test-namespace",
				Spec:      tt.spec,
			}
			got, err := reconciler.Apply(params, tt.name, result)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseTemplates() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseTemplates() got = %v, want %v", got, tt.want)
			}
		})
	}
}
*/
