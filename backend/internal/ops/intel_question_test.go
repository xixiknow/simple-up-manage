package ops

import "testing"

func TestIntelCandyCorrect(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   bool
	}{
		{"21", true},
		{" 21 ", true},
		{"答案是21", true},
		{"答案：21", true},
		{"最终答案是21个糖果。", true},
		{"最少取出21个糖果", true},
		{"至少需要摸出21颗糖", true},
		{"需要抽取21粒", false},
		{"抽取21粒", true},
		{"二十一", true},
		{"２１", true},
		{"The answer is 21.", true},
		{"answer:21", true},
		{"at least 21 candies", true},
		{"结果为21即可", true},
		{"42", false},
		{"121", false},
		{"2 1", false},
		{"21 22", false},
		{"我认为是21，因为西瓜味五角星只有4颗", false},
		{"", false},
		{"不知道", false},
		{"答案：**21**", true},
	} {
		if got := IntelCandyCorrect(tc.answer); got != tc.want {
			t.Errorf("IntelCandyCorrect(%q) = %v, want %v", tc.answer, got, tc.want)
		}
	}
}

func TestIntelPelicanValid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		want   bool
	}{
		{"doctype", "<!DOCTYPE html><html><body></body></html>", true},
		{"html tag", "<html lang=\"zh\"><body>hi</body></html>", true},
		{"svg tag", "<svg viewBox=\"0 0 100 100\"></svg>", true},
		{"svg with attrs", "<svg width='100'>", true},
		{"fenced code block", "```html\n<html><body></body></html>\n```", true},
		{"prose then html", "好的，这是作品：<html></html>", true},
		{"plain text", "抱歉，我无法完成这个任务。", false},
		{"empty", "", false},
		{"broken tag", "<svgwidth=100>", false},
		{"xml only", "<?xml version=\"1.0\"?>", false},
	} {
		if got := IntelPelicanValid(tc.output); got != tc.want {
			t.Errorf("%s: IntelPelicanValid(%q) = %v, want %v", tc.name, tc.output, got, tc.want)
		}
	}
}

func TestIntelQuestionText(t *testing.T) {
	candy := IntelQuestionText("candy", "")
	if candy != IntelCandyPrompt+"\n\n"+IntelCandyContract {
		t.Error("candy default prompt mismatch")
	}
	pelican := IntelQuestionText("pelican", "")
	if pelican != IntelPelicanPrompt+"\n\n"+IntelPelicanContract {
		t.Error("pelican default prompt mismatch")
	}
	if override := IntelQuestionText("candy", "  自定义题目  "); override != "自定义题目" {
		t.Errorf("override prompt = %q", override)
	}
}
