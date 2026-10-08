package domain

import (
	"bytes"
	"mime/multipart"
	"testing"
)

func TestRequestStream(t *testing.T) {
	for _, test := range []struct {
		body          string
		stream, known bool
	}{
		{`{"stream":true}`, true, true}, {`{"stream":false}`, false, true},
		{`{}`, false, true}, {`{"stream":"true"}`, false, false},
		{`{"stream":`, false, false}, {`null`, false, false},
	} {
		stream, known := RequestStream("application/json", []byte(test.body))
		if stream != test.stream || known != test.known {
			t.Fatalf("%s: %v,%v", test.body, stream, known)
		}
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	file, _ := w.CreateFormFile("image", "test.png")
	_, _ = file.Write(bytes.Repeat([]byte{1}, 70000))
	_ = w.WriteField("stream", "true")
	_ = w.Close()
	if stream, known := RequestStream(w.FormDataContentType(), body.Bytes()); !stream || !known {
		t.Fatal("multipart stream was lost")
	}
}

func TestRequestCompaction(t *testing.T) {
	for _, test := range []struct {
		name, path, body string
		want             bool
	}{
		{"trigger final", "/v1/responses", `{"model":"gpt-5","input":[{"type":"message","role":"user"},{"type":"compaction_trigger"}]}`, true},
		{"trigger mid", "/v1/responses", `{"input":[{"type":"compaction_trigger"},{"type":"message","role":"user"}]}`, true},
		{"trigger only", "/v1/responses", `{"input":[{"type":"compaction_trigger"}]}`, true},
		{"no trigger", "/v1/responses", `{"input":[{"type":"message","role":"user"}]}`, false},
		{"input missing", "/v1/responses", `{"model":"gpt-5"}`, false},
		{"input not array", "/v1/responses", `{"input":"compaction_trigger"}`, false},
		{"input item not object", "/v1/responses", `{"input":["compaction_trigger"]}`, false},
		{"malformed json", "/v1/responses", `{"input":[`, false},
		{"wrong path", "/v1/chat/completions", `{"input":[{"type":"compaction_trigger"}]}`, false},
		{"empty body", "/v1/responses", ``, false},
	} {
		if got := RequestCompaction(test.path, []byte(test.body)); got != test.want {
			t.Fatalf("%s: got %v", test.name, got)
		}
	}
}
