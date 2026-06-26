package sse

import (
	"encoding/json"
	"fmt"
)

func Data(v any) ([]byte, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("data: %s\n\n", payload)), nil
}

func Done() []byte {
	return []byte("data: [DONE]\n\n")
}
