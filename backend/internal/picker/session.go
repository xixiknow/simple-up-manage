package picker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

func SessionFromRequest(header string, body []byte) string {
	if s := strings.TrimSpace(header); s != "" {
		return s
	}
	return HashSession(body)
}

func HashSession(body []byte) string {
	var peek struct {
		System   any `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &peek) != nil {
		return ""
	}
	var b strings.Builder
	if peek.System != nil {
		b.WriteString(truncateAny(peek.System, 2048))
		b.WriteByte('\n')
	}
	n := len(peek.Messages)
	if n > 3 {
		n = 3
	}
	for i := 0; i < n; i++ {
		b.WriteString(peek.Messages[i].Role)
		b.WriteByte(':')
		b.WriteString(truncateAny(peek.Messages[i].Content, 1024))
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}

func truncateAny(v any, n int) string {
	s := strings.TrimSpace(fmt.Sprint(v))
	if len(s) > n {
		return s[:n]
	}
	return s
}
