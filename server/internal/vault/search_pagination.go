package vault

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

var ErrInvalidSearchCursor = errors.New("search continuation is invalid or incompatible")

const searchCursorVersion = 1

type searchCursor struct {
	Version int    `json:"version"`
	Query   string `json:"query"`
	Offset  int64  `json:"offset"`
}

func encodeSearchCursor(query string, offset int64) string {
	if offset <= 0 {
		return ""
	}
	encoded, _ := json.Marshal(searchCursor{
		Version: searchCursorVersion,
		Query:   query,
		Offset:  offset,
	})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeSearchCursor(query, token string) (int64, error) {
	if token == "" {
		return 0, nil
	}
	encoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, ErrInvalidSearchCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var cursor searchCursor
	if err := decoder.Decode(&cursor); err != nil {
		return 0, ErrInvalidSearchCursor
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return 0, ErrInvalidSearchCursor
	}
	if cursor.Version != searchCursorVersion || cursor.Query != query || cursor.Offset <= 0 {
		return 0, ErrInvalidSearchCursor
	}
	return cursor.Offset, nil
}
