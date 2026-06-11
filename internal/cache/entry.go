package cache

import (
	"encoding/json"
	"net/http"
	"time"
)

type EntryMeta struct {
	Method     string      `json:"method"`
	URL        string      `json:"url"`
	StatusCode int         `json:"status_code"`
	Header     http.Header `json:"header"`
	// CachedAt is when the entry was written. Zero value (e.g. from a
	// legacy entry serialized before this field existed) is interpreted by
	// the revalidate logic as "immediately stale".
	CachedAt time.Time `json:"cached_at,omitempty"`
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
