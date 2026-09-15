package handler

import (
	"mime"
	"simple-up-manage/internal/upstream"
)

func isTextAPI(path string) bool {
	return path == "/v1/responses" || path == "/v1/chat/completions" || path == "/v1/messages"
}
func isJSONContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && mt == "application/json"
}

func validateJSONResponse(path string, raw []byte) (string, error) {
	return upstream.ValidateJSONResponse(path, raw)
}

func (s *streamCollector) validateEvent(name, data string) {
	v := upstream.StreamValidator{Path: s.protocolPath, Strict: s.strict, SawValid: s.sawValid, ChatFinished: s.chatFinished, ResponseID: s.responseID, Err: s.terminalErr}
	v.Event(name, data)
	s.sawValid = v.SawValid
	s.chatFinished = v.ChatFinished
	s.responseID = v.ResponseID
	s.terminalErr = v.Err
}
