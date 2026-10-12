package run

import (
	"crypto/sha256"
	"encoding/hex"
)

type RenderedInputHash string

func RenderedInputHashFor(data []byte) RenderedInputHash {
	digest := sha256.Sum256(data)
	return RenderedInputHash(hex.EncodeToString(digest[:]))
}

func (hash RenderedInputHash) Matches(data []byte) bool {
	return lowercaseSHA256Hex(string(hash)) && hash == RenderedInputHashFor(data)
}
