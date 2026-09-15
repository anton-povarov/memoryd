package vault

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestBlobrefCanonicalRoundTrip(t *testing.T) {
	digest := sha256.Sum256([]byte("content"))
	ref := NewSHA256Blobref(digest)
	if !strings.HasPrefix(ref.String(), "sha256-") {
		t.Fatalf("Blobref = %q", ref)
	}
	parsed, err := ParseBlobref(ref.String())
	if err != nil || parsed != ref {
		t.Fatalf("round trip = %v, error = %v", parsed, err)
	}
	for _, invalid := range []string{
		ref.digestHex(),
		"md5-" + ref.digestHex(),
		"sha256-" + strings.ToUpper(ref.digestHex()),
		"sha256-" + strings.Repeat("x", sha256.Size*2),
		"sha256-short",
	} {
		if _, err := ParseBlobref(invalid); err == nil {
			t.Errorf("accepted invalid Blobref %q", invalid)
		}
	}
}
