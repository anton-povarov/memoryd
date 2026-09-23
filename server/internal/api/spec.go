package api

import (
	"encoding/json"
	"fmt"
)

// OpenAPIJSON serializes the specification embedded in the generated bindings.
func OpenAPIJSON() ([]byte, error) {
	specification, err := GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load embedded OpenAPI specification: %w", err)
	}
	jsonSpecification, err := json.Marshal(specification)
	if err != nil {
		return nil, fmt.Errorf("serialize embedded OpenAPI specification: %w", err)
	}
	return jsonSpecification, nil
}
