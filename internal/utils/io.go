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
// of the specified length, base64 encoded for safe string usage
func RandBytes(n int) string {
	buf := make([]byte, n)
	rand.Read(buf)
	return base64.StdEncoding.EncodeToString(buf)
}

// QuantityToString converts a resource.Quantity to a string
func QuantityToString(q resource.Quantity) string {
	return q.String()
}
