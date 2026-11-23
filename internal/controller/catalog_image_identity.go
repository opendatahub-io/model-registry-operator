package controller

import "github.com/distribution/reference"

func sameCatalogImageReference(actual, desired string) bool {
	if actual == desired {
		return actual != ""
	}
	actualRef, err := reference.ParseNormalizedNamed(actual)
	if err != nil {
		return false
	}
	desiredRef, err := reference.ParseNormalizedNamed(desired)
	if err != nil {
		return false
	}
	actualDigest, actualOK := actualRef.(reference.Digested)
	desiredDigest, desiredOK := desiredRef.(reference.Digested)
	return actualOK && desiredOK &&
		actualDigest.Digest().Algorithm() == "sha256" &&
		desiredDigest.Digest().Algorithm() == "sha256" &&
		actualDigest.Digest() == desiredDigest.Digest()
}
