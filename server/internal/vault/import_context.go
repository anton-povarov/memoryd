package vault

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidImportContext = errors.New("invalid Import Context")

// Normalized applies the same filename and timestamp rules as Vault imports.
func (context ImportContext) Normalized() (ImportContext, error) {
	if strings.TrimSpace(context.OriginalFilename) == "" {
		return ImportContext{}, fmt.Errorf(
			"%w: original_filename is required", ErrInvalidImportContext,
		)
	}
	return ImportContext{
		OriginalFilename:   safeFilename(context.OriginalFilename),
		RelativePath:       context.RelativePath,
		FullPath:           context.FullPath,
		FilesystemCreated:  normalizeTime(context.FilesystemCreated),
		FilesystemModified: normalizeTime(context.FilesystemModified),
	}, nil
}
