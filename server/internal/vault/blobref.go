package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	sha256Prefix  = "sha256-"
	sha256HexSize = sha256.Size * 2
)

// Blobref is a content address. Its only supported hash function is SHA-256.
// Keeping the digest as bytes prevents an unprefixed or malformed value from
// being constructed by callers.
type Blobref struct {
	digest [sha256.Size]byte
}

func NewSHA256Blobref(digest [sha256.Size]byte) Blobref {
	return Blobref{digest: digest}
}

func ParseBlobref(value string) (Blobref, error) {
	if !strings.HasPrefix(value, sha256Prefix) ||
		len(value) != len(sha256Prefix)+sha256HexSize {
		return Blobref{}, errors.New("invalid SHA-256 Blobref")
	}
	hexDigest := strings.TrimPrefix(value, sha256Prefix)
	if strings.ToLower(hexDigest) != hexDigest {
		return Blobref{}, errors.New("Blobref digest must be lowercase")
	}
	digest, err := hex.DecodeString(hexDigest)
	if err != nil {
		return Blobref{}, fmt.Errorf("decode Blobref digest: %w", err)
	}
	var fixed [sha256.Size]byte
	copy(fixed[:], digest)
	return NewSHA256Blobref(fixed), nil
}

func (ref Blobref) String() string {
	return sha256Prefix + ref.digestHex()
}

func (ref Blobref) digestHex() string {
	return hex.EncodeToString(ref.digest[:])
}
