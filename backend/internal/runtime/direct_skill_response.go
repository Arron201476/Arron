package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func directSkillStepResponseContract(outputs []ContextOutputContract, provider *ContextOutputContract) (*ContextOutputContract, error) {
	if len(outputs) == 0 {
		return nil, fmt.Errorf("direct Skill output contracts are missing")
	}
	if len(outputs) == 1 {
		return directSkillResponseContract(outputs[0], provider)
	}
	properties := make(map[string]json.RawMessage, len(outputs))
	required := make([]string, 0, len(outputs))
	for _, output := range outputs {
		if output.ArtifactType == "" || properties[output.ArtifactType] != nil {
			return nil, fmt.Errorf("direct Skill output types must be nonempty and unique")
		}
		properties[output.ArtifactType] = output.Schema
		required = append(required, output.ArtifactType)
	}
	schema, err := json.Marshal(map[string]any{
		"type": "object", "properties": properties, "required": required, "additionalProperties": false,
	})
	if err != nil {
		return nil, err
	}
	output := outputs[0]
	output.Schema = schema
	return directSkillResponseContract(output, provider)
}

func directSkillResponseContract(output ContextOutputContract, provider *ContextOutputContract) (*ContextOutputContract, error) {
	result := output
	result.ArtifactType = "provider_result"
	if provider == nil || bytes.Equal(output.Schema, provider.Schema) {
		return &result, nil
	}
	// There is no response adapter for a managed Skill: the same object must
	// satisfy its transport constraints and the artifact's stored schema.
	schema, err := json.Marshal(map[string]any{
		"type":  "object",
		"allOf": []json.RawMessage{output.Schema, provider.Schema},
	})
	if err != nil {
		return nil, err
	}
	result.Schema = schema
	return &result, nil
}

func validateDirectSkillResponse(pack StepExecutionContextPack, payload json.RawMessage) error {
	if len(pack.OutputContracts) > 1 {
		contract, err := directSkillStepResponseContract(pack.OutputContracts, pack.ProviderResultContract)
		if err != nil {
			return err
		}
		return validateEmbeddedJSONSchema(contract.Schema, payload)
	}
	if err := validateEmbeddedJSONSchema(pack.OutputContract.Schema, payload); err != nil {
		return err
	}
	if pack.ProviderResultContract != nil {
		return validateEmbeddedJSONSchema(pack.ProviderResultContract.Schema, payload)
	}
	return nil
}
