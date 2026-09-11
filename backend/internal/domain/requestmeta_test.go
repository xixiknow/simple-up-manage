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
