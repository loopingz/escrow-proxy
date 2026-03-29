package archive

import "strings"

func DetectFormat(dest string) string {
	if strings.HasSuffix(dest, ".tar.gz") || strings.HasSuffix(dest, ".tgz") {
		return "tgz"
	}
	if looksLikeRegistryRef(dest) {
		return "oci"
	}
	return "cas"
}

func looksLikeRegistryRef(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return false
	}
	if !strings.Contains(s, "/") {
		return false
	}
	return true
}

func NewFormat(name string, ociEntriesPerLayer int) Format {
	switch name {
	case "tgz":
		return &TarGzFormat{}
	case "oci":
		return &OCIFormat{EntriesPerLayer: ociEntriesPerLayer}
	case "cas":
		return &CASFormat{}
	default:
		return &TarGzFormat{}
	}
}

func NewFormatFromDest(dest string, ociEntriesPerLayer int) Format {
	return NewFormat(DetectFormat(dest), ociEntriesPerLayer)
}
