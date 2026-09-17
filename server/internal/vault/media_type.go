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

var ErrInvalidMediaType = errors.New("invalid multipart file Content-Type")

// resolveMediaType classifies staged bytes before their Memory metadata commits.
// Generic detector results allow a specific client declaration to supply a type.
func resolveMediaType(path, declared string) (string, error) {
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
	if declared == "" {
		return mediaType, nil
	}

	parsed, _, err := mime.ParseMediaType(declared)
	if err != nil {
		return "", fmt.Errorf("%w %q: %v", ErrInvalidMediaType, declared, err)
	}
	if strings.Count(parsed, "/") != 1 || strings.Contains(parsed, "*") {
		return "", fmt.Errorf("%w %q: expected type/subtype", ErrInvalidMediaType, declared)
	}
	if mediaType == plainTextMediaType && parsed == octetStreamMediaType {
		return mediaType, nil
	}
	return parsed, nil
}
