package cairn

import (
	"crypto/sha256"
	"encoding/hex"
)

// OfflineScope partitions browser copies when a NAS changes its backend or
// account credential. Only a domain-separated digest is exposed to the browser.
func (c *Client) OfflineScope() string {
	sum := sha256.Sum256([]byte("cairn:offline:v1\x00" + c.baseURL + "\x00" + c.token))
	return hex.EncodeToString(sum[:])
}
