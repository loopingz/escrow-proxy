package cache

import (
	"encoding/json"
	"net/http"
)

type EntryMeta struct {
	Method     string      `json:"method"`
	URL        string      `json:"url"`
	StatusCode int         `json:"status_code"`
	Header     http.Header `json:"header"`
}

func MarshalMeta(meta *EntryMeta) ([]byte, error) {
	return json.Marshal(meta)
}

func UnmarshalMeta(data []byte) (*EntryMeta, error) {
	var meta EntryMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}
