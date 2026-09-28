package agent

import (
	"fmt"
	"sort"
	"strings"

	"tradeforge/pkg/types"
)

// timeframePhrases 把常见的中文/英文周期说法映射到 types.Timeframe，用于检测用户
// 原话里明确提到过哪些周期。这是一道兜底检查用的启发式匹配，不是精确的自然语言
// 理解——它的作用只是发现"用户明明说了两个不同的周期各管一段判断逻辑，模型翻译出
// 的配置却把其中一个悄悄弄丢了、让对应模块的判断跟着触发周期走"这种落差，而不是
// 追求完美召回。
var timeframePhrases = map[types.Timeframe][]string{
	types.TF1m:  {"1分钟", "一分钟", "1m"},
	types.TF5m:  {"5分钟", "五分钟", "5m"},
	types.TF15m: {"15分钟", "十五分钟", "15m"},
	types.TF1h:  {"1小时", "一小时", "一个小时", "1个小时", "1h"},
	types.TF4h:  {"4小时", "四小时", "4h"},
	types.TF1d:  {"1天", "一天", "日线", "1d"},
}

// mentionedTimeframes 返回文本里明确提到的周期集合。
//
// 短语按长度从长到短依次匹配，命中后把对应的文本区间"挖空"再继续——像
// "15分钟" 天生就包含 "5分钟" 这个子串，不先处理长的会把 15 分钟误判成
// 也提到了 5 分钟。挖空是最简单能同时处理阿拉伯数字和中文数字写法的办法，
// 不需要为两套数字系统分别写正则。
func mentionedTimeframes(text string) map[types.Timeframe]bool {
	type candidate struct {
		tf     types.Timeframe
		phrase []rune
	}
	var candidates []candidate
	for tf, phrases := range timeframePhrases {
		for _, p := range phrases {
			candidates = append(candidates, candidate{tf, []rune(strings.ToLower(p))})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return len(candidates[i].phrase) > len(candidates[j].phrase) })

	runes := []rune(strings.ToLower(text))
	out := map[types.Timeframe]bool{}
	for _, c := range candidates {
		p := c.phrase
		for i := 0; i+len(p) <= len(runes); i++ {
			matched := true
			for j, r := range p {
				if runes[i+j] != r {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			out[c.tf] = true
			for j := range p {
				runes[i+j] = ' '
			}
		}
	}
	return out
}

// checkTimeframeCoverage 核对：如果用户原话里明确提到了不止一个周期（说明这是一条
// 多周期协同的规则），最终配置实际用到的周期集合（cfg.RequiredTimeframes）必须把
// 提到的周期都覆盖到——不能出现"用户说了 1 小时判断背景、15 分钟判断触发，配置里
// 却完全没有任何模块用 1 小时"这种整段判断逻辑的周期被悄悄弄丢的情况。
//
// 只在"用户明确提到的周期数 > 1"且确实有遗漏时才拒绝：用户只说了一个周期，或者
// 提到的周期本来就都在配置里用到了，都直接放过，不为了这道兜底检查制造额外的
// 误判和无谓重试。
func checkTimeframeCoverage(cfg types.StrategyConfig, allUserText string) error {
	mentioned := mentionedTimeframes(allUserText)
	if len(mentioned) < 2 {
		return nil
	}

	used := map[types.Timeframe]bool{}
	for _, tf := range cfg.RequiredTimeframes() {
		used[tf] = true
	}

	var missing []string
	for tf := range mentioned {
		if !used[tf] {
			missing = append(missing, string(tf))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf(
		"用户的描述里明确提到了不止一个周期，但配置里完全没有任何模块使用 %s——"+
			"这条（或这些）判断逻辑的周期被漏翻了，不能让它悄悄跟着触发周期走，"+
			"必须回到用户原话，把对应模块的 timeframe 字段显式填成用户实际说的那个周期",
		strings.Join(missing, "、"))
}
