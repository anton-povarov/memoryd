package vault

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidImportContext = errors.New("invalid Import Context")

// ImportContextInput is the client-observed metadata before timestamp validation.
// Empty optional strings mean that the client could not provide a value.
type ImportContextInput struct {
	OriginalFilename     string `json:"-"`
	RelativePath         string `json:"relative_path"`
	FullPath             string `json:"full_path"`
	FilesystemCreatedAt  string `json:"filesystem_created_at"`
	FilesystemModifiedAt string `json:"filesystem_modified_at"`
}

func ParseImportContext(input ImportContextInput) (ImportContext, error) {
	created, err := parseImportTime("filesystem_created_at", input.FilesystemCreatedAt)
	if err != nil {
		return ImportContext{}, err
	}
	modified, err := parseImportTime("filesystem_modified_at", input.FilesystemModifiedAt)
	if err != nil {
		return ImportContext{}, err
	}
	return (ImportContext{
		OriginalFilename:   input.OriginalFilename,
		RelativePath:       input.RelativePath,
		FullPath:           input.FullPath,
		FilesystemCreated:  created,
		FilesystemModified: modified,
	}).normalized()
}

func parseImportTime(name, value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, fmt.Errorf("%w: %s must be RFC3339", ErrInvalidImportContext, name)
	}
	return normalizeTime(&parsed), nil
}

func (context ImportContext) normalized() (ImportContext, error) {
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

// ImportFact is a provenance assertion derived from the stored Import Context.
type ImportFact struct {
	Namespace string
	Name      string
	Value     any
	ValueType string
	Origin    string
}

// Facts presents the available client-observed provenance. It can be called on
// a context loaded from the Vault, including Memories imported before this code.
func (context ImportContext) Facts() []ImportFact {
	facts := []ImportFact{importFact("original_filename", context.OriginalFilename, "string")}
	if context.RelativePath != "" {
		facts = append(facts, importFact("relative_path", context.RelativePath, "string"))
	}
	if context.FullPath != "" {
		facts = append(facts, importFact("full_path", context.FullPath, "string"))
	}
	if context.FilesystemCreated != nil {
		facts = append(facts, importFact(
			"filesystem_created_at",
			context.FilesystemCreated.Format(time.RFC3339Nano),
			"datetime",
		))
	}
	if context.FilesystemModified != nil {
		facts = append(facts, importFact(
			"filesystem_modified_at",
			context.FilesystemModified.Format(time.RFC3339Nano),
			"datetime",
		))
	}
	return facts
}

func importFact(name string, value any, valueType string) ImportFact {
	return ImportFact{
		Namespace: "import",
		Name:      name,
		Value:     value,
		ValueType: valueType,
		Origin:    "import-client",
	}
}
