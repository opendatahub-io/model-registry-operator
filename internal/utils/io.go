package utils

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"

	"k8s.io/apimachinery/pkg/api/resource"
)

func DownloadFile(url string, path string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("unable to fetch %q: %s", url, resp.Status)
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}

	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

// RandBytes generates a cryptographically secure random byte sequence
// of the specified length, base64 URL-encoded for safe string usage in URLs.
// Returns an error if the system's secure random number generator fails.
func RandBytes(n int) (string, error) {
	buf := make([]byte, n)
	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return base64.URLEncoding.EncodeToString(buf), nil
}

// QuantityToString converts a resource.Quantity to a string
func QuantityToString(q resource.Quantity) string {
	return q.String()
}
