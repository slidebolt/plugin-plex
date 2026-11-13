package plex

import "encoding/base64"

func base64Raw(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }
func base64Decode(v string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(v)
	return string(b), err
}
