package vault

import (
	"errors"
	"fmt"
	"mime"
	"strings"

	"github.com/gabriel-vasile/mimetype"
)

const (
	octetStreamMediaType = "application/octet-stream"
	plainTextMediaType   = "text/plain"
)

var ErrInvalidMediaType = errors.New("invalid Media Type hint")

// resolveMediaType classifies staged bytes before their Memory metadata commits.
// Generic detector results allow a specific importer hint to supply a type.
func resolveMediaType(path, hint string) (string, error) {
	detected, err := mimetype.DetectFile(path)
	if err != nil {
		return "", fmt.Errorf("detect Blob media type: %w", err)
	}
	mediaType, _, err := mime.ParseMediaType(detected.String())
	if err != nil {
		return "", fmt.Errorf("parse detected Blob media type: %w", err)
	}
	if mediaType != octetStreamMediaType && mediaType != plainTextMediaType {
		return mediaType, nil
	}
	if hint == "" {
		return mediaType, nil
	}

	parsed, _, err := mime.ParseMediaType(hint)
	if err != nil {
		return "", fmt.Errorf("%w %q: %v", ErrInvalidMediaType, hint, err)
	}
	if strings.Count(parsed, "/") != 1 || strings.Contains(parsed, "*") {
		return "", fmt.Errorf("%w %q: expected type/subtype", ErrInvalidMediaType, hint)
	}
	if mediaType == plainTextMediaType && parsed == octetStreamMediaType {
		return mediaType, nil
	}
	return parsed, nil
}
