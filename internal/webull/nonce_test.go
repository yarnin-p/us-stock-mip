package webull

import (
	"regexp"
	"testing"
)

func TestRandomNonceReturnsUUID(t *testing.T) {
	t.Parallel()
	nonce, err := randomNonce()
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(
		`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
	)
	if !pattern.MatchString(nonce) {
		t.Fatalf("nonce is not a UUID v4: %q", nonce)
	}
}
