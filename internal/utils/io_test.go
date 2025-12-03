package utils

import (
	"regexp"
	"testing"
)

func TestRandBytes(t *testing.T) {
	tests := []struct {
		name   string
		length int
	}{
		{
			name:   "16 bytes",
			length: 16,
		},
		{
			name:   "32 bytes",
			length: 32,
		},
		{
			name:   "8 bytes",
			length: 8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RandBytes(tt.length)

			// Check that result is not empty
			if len(result) == 0 {
				t.Errorf("RandBytes() returned empty string")
			}

			// URL-safe base64 should only contain: A-Z, a-z, 0-9, -, _, =
			// No forward slashes or plus signs
			urlSafePattern := regexp.MustCompile(`^[A-Za-z0-9_-]+=*$`)
			if !urlSafePattern.MatchString(result) {
				t.Errorf("RandBytes() = %q contains non-URL-safe characters", result)
			}

			// Verify no forward slashes (the bug we're fixing)
			if regexp.MustCompile(`/`).MatchString(result) {
				t.Errorf("RandBytes() = %q contains forward slashes, not URL-safe", result)
			}

			// Verify no plus signs (also not URL-safe)
			if regexp.MustCompile(`\+`).MatchString(result) {
				t.Errorf("RandBytes() = %q contains plus signs, not URL-safe", result)
			}
		})
	}
}

func TestRandBytesUniqueness(t *testing.T) {
	// Generate multiple random values to ensure they're different
	const iterations = 100
	seen := make(map[string]bool)

	for i := 0; i < iterations; i++ {
		result := RandBytes(16)
		if seen[result] {
			t.Errorf("RandBytes() generated duplicate value: %q", result)
		}
		seen[result] = true
	}

	if len(seen) != iterations {
		t.Errorf("Expected %d unique values, got %d", iterations, len(seen))
	}
}
