package ops

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"simple-up-manage/internal/domain"
)

// ProbeOptions only applies to this invocation; scheduled defaults stay intact.
type ProbeOptions struct {
	Model    string  `json:"model"`
	Prompt   *string `json:"prompt"`
	Protocol string  `json:"protocol"`
}

func (o ProbeOptions) Validate(deep bool) error {
	if !deep && (o.Model != "" || o.Prompt != nil || o.Protocol != "") {
		return fmt.Errorf("model, prompt and protocol require a deep probe")
	}
	if utf8.RuneCountInString(o.Model) > 256 {
		return fmt.Errorf("model must be at most 256 characters")
	}
	if o.Prompt != nil && (strings.TrimSpace(*o.Prompt) == "" || utf8.RuneCountInString(*o.Prompt) > 4000) {
		return fmt.Errorf("prompt must contain 1 to 4000 characters")
	}
	if o.Protocol != "" && o.Protocol != domain.ProtocolOpenAI && o.Protocol != domain.ProtocolAnthropic {
		return fmt.Errorf("protocol must be openai or anthropic")
	}
	return nil
}

func firstProbeOptions(options []ProbeOptions) ProbeOptions {
	if len(options) == 0 {
		return ProbeOptions{}
	}
	o := options[0]
	o.Model = strings.TrimSpace(o.Model)
	return o
}
