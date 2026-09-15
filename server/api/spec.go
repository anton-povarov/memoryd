package api

import (
	"encoding/json"
	"fmt"

	"sigs.k8s.io/yaml"
)

// OpenAPIYAML serializes the specification embedded in the generated bindings.
func OpenAPIYAML() ([]byte, error) {
	specification, err := GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("load embedded OpenAPI specification: %w", err)
	}
	jsonSpecification, err := json.Marshal(specification)
	if err != nil {
		return nil, fmt.Errorf("serialize embedded OpenAPI specification: %w", err)
	}
	yamlSpecification, err := yaml.JSONToYAML(jsonSpecification)
	if err != nil {
		return nil, fmt.Errorf("serialize embedded OpenAPI YAML: %w", err)
	}
	return yamlSpecification, nil
}
