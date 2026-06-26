package codex

import (
	"encoding/json"
	"fmt"
	"os"
)

type Profile struct {
	Model             string `json:"model"`
	Instructions      string `json:"instructions"`
	Tools             any    `json:"tools"`
	ToolChoice        any    `json:"tool_choice"`
	ParallelToolCalls any    `json:"parallel_tool_calls"`
	Include           any    `json:"include"`
}

type Scaffold struct {
	DeveloperItem      any    `json:"developer_item"`
	EnvironmentContext string `json:"environment_context"`
}

func LoadProfile(path string) (Profile, error) {
	var profile Profile
	if err := loadJSON(path, &profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func LoadScaffold(path string) (Scaffold, error) {
	var scaffold Scaffold
	if err := loadJSON(path, &scaffold); err != nil {
		return Scaffold{}, err
	}
	return scaffold, nil
}

func loadJSON(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("load %s: %w", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
