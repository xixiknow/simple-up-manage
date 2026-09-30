package upstream

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestSSEReaderPreservesFraming(t *testing.T) {
	for _, body := range []string{
		"data: {\"type\":\"response.created\"}\nevent: response.created\n\n",
		"event: response.created\ndata: {\n" + "data: \"type\":\"response.created\"}\n\n",
		": heartbeat\r\nid: 12\r\nevent: ping\r\ndata: {\"type\":\"ping\"}\r\n\r\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}",
	} {
		raw, err := io.ReadAll(NewSSEReader(iotest.OneByteReader(strings.NewReader(body))))
		if err != nil || string(raw) != body {
			t.Fatalf("body=%q error=%v got=%q", body, err, raw)
		}
	}
}

func TestSSEReaderBoundsWholeEvent(t *testing.T) {
	// Many short lines must obey the same bound as a single oversized line.
	r := io.MultiReader(strings.NewReader("event: ping\n"), io.LimitReader(repeatSSEComment{}, MaxSSEEventBytes))
	_, err := io.Copy(io.Discard, NewSSEReader(r))
	if !errors.Is(err, ErrSSEEventTooLarge) {
		t.Fatalf("unbounded event: %v", err)
	}
}

type repeatSSEComment struct{}

func (repeatSSEComment) Read(p []byte) (int, error) {
	return copy(p, ": heartbeat\n"), nil
}
