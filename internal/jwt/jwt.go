// Package jwt decodes JWT payloads without verifying them. dial only reads
// claims from tokens the CLIs already trust; it never makes auth decisions
// from them.
package jwt

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// Claims decodes the payload segment of token into a generic map.
func Claims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, errors.New("not a JWT")
	}
	seg := parts[1]
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(seg, "="))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// Exp returns the exp claim in unix seconds, or 0.
func Exp(token string) int64 {
	c, err := Claims(token)
	if err != nil {
		return 0
	}
	if f, ok := c["exp"].(float64); ok {
		return int64(f)
	}
	return 0
}
