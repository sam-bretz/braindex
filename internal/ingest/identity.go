package ingest

import (
	"crypto/sha256"
	"fmt"
)

// StableID namespaces external identifiers for deduplication.
func StableID(kind, value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%s-%x", kind, sum[:16])
}
