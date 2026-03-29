package proxy

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

func ComputeCacheKey(req *http.Request, selectedHeaders []string) string {
	h := sha256.New()
	h.Write([]byte(req.Method))
	h.Write([]byte("\n"))
	h.Write([]byte(req.URL.String()))
	h.Write([]byte("\n"))

	sorted := make([]string, len(selectedHeaders))
	copy(sorted, selectedHeaders)
	sort.Strings(sorted)

	var headerParts []string
	for _, name := range sorted {
		vals := req.Header.Values(name)
		sort.Strings(vals)
		for _, v := range vals {
			headerParts = append(headerParts, fmt.Sprintf("%s:%s", strings.ToLower(name), v))
		}
	}
	h.Write([]byte(strings.Join(headerParts, "\n")))
	h.Write([]byte("\n"))

	if req.Body != nil && req.Body != http.NoBody {
		bodyHash := sha256.New()
		io.Copy(bodyHash, req.Body)
		h.Write(bodyHash.Sum(nil))
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}
