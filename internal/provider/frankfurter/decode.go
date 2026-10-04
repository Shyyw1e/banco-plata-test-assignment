package frankfurter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func decodeRate(data []byte) (*rateResponse, error) {
	invalid := errors.New("invalid provider object")
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return nil, invalid
	}
	fields := make(map[string]json.RawMessage, 4)
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return nil, invalid
		}
		name, ok := token.(string)
		if !ok {
			return nil, invalid
		}
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil {
			return nil, invalid
		}
		switch name {
		case "date", "base", "quote", "rate":
			if _, exists := fields[name]; exists {
				return nil, invalid
			}
			fields[name] = bytes.TrimSpace(raw)
		}
	}
	token, err = dec.Token()
	if err != nil || token != json.Delim('}') {
		return nil, invalid
	}
	if _, err = dec.Token(); err != io.EOF {
		return nil, invalid
	}
	if len(fields) != 4 {
		return nil, invalid
	}
	result := &rateResponse{}
	for name, dst := range map[string]*string{"date": &result.Date, "base": &result.Base, "quote": &result.Quote} {
		raw := fields[name]
		if len(raw) == 0 || raw[0] != '"' {
			return nil, invalid
		}
		if err = json.Unmarshal(raw, dst); err != nil {
			return nil, invalid
		}
	}
	raw := fields["rate"]
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return nil, invalid
	}
	result.Rate = raw
	return result, nil
}
