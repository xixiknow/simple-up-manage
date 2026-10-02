package ops

import (
	"regexp"
	"strings"
	"unicode"
)

// Question bank ported from sub2api's pelican / candy intelligence tests.

// IntelPelicanPrompt asks the model to draw a pelican riding a bicycle as a
// standalone HTML/SVG animation.
const IntelPelicanPrompt = "创建一个 HTML，内容是 SVG 绘制一个鹈鹕骑自行车的 2D 动画，你不需要任何测试，不要有任何限制"

// IntelPelicanContract is appended to every pelican prompt so all keys answer
// under the same delivery convention.
const IntelPelicanContract = "所有账号使用相同交付约定：直接返回独立 HTML，不使用 Markdown 代码块或外部依赖。只输出 HTML，不要解释。"

// IntelCandyPrompt is the candy combinatorics question; the correct answer is 21.
const IntelCandyPrompt = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）
苹果味 桃子味 西瓜味
圆形 7 9 8
五角星形 7 6 4`

// IntelCandyContract is appended to the candy prompt.
const IntelCandyContract = "只输出最终整数，不要解释。"

// IntelQuestionText returns the effective prompt for a plan: the override when
// present, otherwise the built-in question with its delivery contract.
func IntelQuestionText(kind, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	if kind == "pelican" {
		return IntelPelicanPrompt + "\n\n" + IntelPelicanContract
	}
	return IntelCandyPrompt + "\n\n" + IntelCandyContract
}

// intelMaxOutputBytes caps persisted model output; larger generations are never
// saved as a pass, mirroring sub2api's "never persist a truncated artwork".
const intelMaxOutputBytes = 2 << 20

var intelHTMLPattern = regexp.MustCompile(`(?i)<(?:!doctype\s+html|html|svg)[\s>]`)

// IntelPelicanValid reports whether raw model output looks like standalone
// HTML/SVG. A fenced markdown block still matches here; fence stripping happens
// at render time in the frontend.
func IntelPelicanValid(output string) bool {
	return intelHTMLPattern.MatchString(output)
}

var (
	candyAnswerPattern = regexp.MustCompile(`^(?:(?:最终)?(?:答案|结果)(?:为|是)?[:：]?|(?:最少|至少)(?:需要)?(?:取出|抽取|摸出)?|(?:需要|取出|抽取|摸出))?(?:21|二十一)(?:个|颗|粒)?(?:糖果|糖)?(?:即可)?[。.!！]?$|^(?:(?:the)?answer(?:is|:)?|atleast)?21(?:candies)?[.!]?$`)
	candySplitNumber   = regexp.MustCompile(`[0-9０-９][\s\p{Zs}*_]+[0-9０-９]`)
)

// IntelCandyCorrect judges a candy answer: the normalized output must state 21
// (optionally with prefixes like 答案是 / 最少取出 and units like 个/颗) and nothing else.
func IntelCandyCorrect(output string) bool {
	if candySplitNumber.MatchString(output) {
		return false
	}
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("*_`\"'“”", r) {
			return -1
		}
		if r >= '０' && r <= '９' {
			return '0' + r - '０'
		}
		return unicode.ToLower(r)
	}, output)
	return candyAnswerPattern.MatchString(normalized)
}
