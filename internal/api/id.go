package api

import (
	"crypto/rand"
	"encoding/hex"
)

func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
