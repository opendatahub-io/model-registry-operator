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
