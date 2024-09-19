package controller

import "strings"

func getAuthConfigLabels(defaultAuthConfigLabelsString string) map[string]string {
	defaultAuthConfigLabels := make(map[string]string)
	if len(defaultAuthConfigLabelsString) != 0 {
		// split key=value pairs separated by commas
		pairs := strings.Split(defaultAuthConfigLabelsString, ",")
		for _, pair := range pairs {
			// split key value pair
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) > 0 {
				key := parts[0]
				var value string
				if len(parts) > 1 {
					value = parts[1]
				}
				defaultAuthConfigLabels[key] = value
			}
		}
	}
	return defaultAuthConfigLabels
}
