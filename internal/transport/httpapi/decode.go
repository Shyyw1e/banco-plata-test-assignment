package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func decodeCreate(data []byte) (createRequest, error) {
	var result createRequest
	invalid := errors.New("invalid JSON object")
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return result, invalid
	}
	seen := false
	for dec.More() {
		token, err = dec.Token()
		if err != nil || token != "pair" || seen {
			return result, invalid
		}
		seen = true
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil {
			return result, invalid
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] != '"' {
			return result, invalid
		}
		if err = json.Unmarshal(raw, &result.Pair); err != nil {
			return result, invalid
		}
	}
	token, err = dec.Token()
	if err != nil || token != json.Delim('}') {
		return result, invalid
	}
	if _, err = dec.Token(); err != io.EOF {
		return result, invalid
	}
	return result, nil
}
