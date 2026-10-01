package agent

import (
	"fmt"
	"sort"
	"strings"

	"tradeforge/pkg/types"
)

// timeframePhrases maps common Chinese/English ways of naming a timeframe to
// types.Timeframe, used to detect which timeframes the user's own words
// explicitly mentioned. This is heuristic matching for a backstop check, not
// precise natural-language understanding — its job is only to catch the gap
// where "the user clearly named two different timeframes, each governing a
// separate piece of logic, but the model's translated config silently
// dropped one of them, letting that module's logic quietly follow the
// trigger timeframe instead" — it isn't trying for perfect recall.
//
// Both language's phrasings are needed regardless of which UI language is
// selected: a user can type their strategy description in either language
// (this is natural-language input the user typed, not page chrome that
// follows the viewer's language toggle), so this backstop has to recognize
// both to catch the same class of bug for either language's users.
var timeframePhrases = map[types.Timeframe][]string{
	types.TF1m:  {"1分钟", "一分钟", "1m", "1 minute", "one minute", "1min"},
	types.TF5m:  {"5分钟", "五分钟", "5m", "5 minute", "5 minutes", "five minutes", "5min"},
	types.TF15m: {"15分钟", "十五分钟", "15m", "15 minute", "15 minutes", "fifteen minutes", "15min"},
	types.TF1h:  {"1小时", "一小时", "一个小时", "1个小时", "1h", "1 hour", "one hour", "1hr", "hourly"},
	types.TF4h:  {"4小时", "四小时", "4h", "4 hour", "4 hours", "four hours", "4hr"},
	types.TF1d:  {"1天", "一天", "日线", "1d", "1 day", "one day", "daily"},
}

// mentionedTimeframes returns the set of timeframes explicitly mentioned in
// the text.
//
// Phrases are matched longest-first, and once a match hits, that span of text
// is "blanked out" before continuing — since "15分钟" (15 minutes) naturally
// contains "5分钟" (5 minutes) as a substring, matching the longer phrase
// first is required, otherwise 15 minutes would be misread as also
// mentioning 5 minutes. Blanking out matched spans is the simplest way to
// handle both Arabic numerals and Chinese numeral writing at once, without
// writing separate regexes for the two numbering systems.
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

// checkTimeframeCoverage verifies: if the user's own words explicitly
// mentioned more than one timeframe (implying this is a rule coordinating
// across multiple timeframes), the set of timeframes actually used by the
// final config (cfg.RequiredTimeframes) must cover every one mentioned — it
// must not happen that "the user said 1h for the background condition and
// 15m for the trigger, but the config has no module using 1h at all", i.e.
// an entire piece of logic's timeframe silently dropped.
//
// Only rejects when "the user explicitly mentioned more than 1 timeframe"
// AND something is actually missing: if the user only mentioned one
// timeframe, or every mentioned timeframe is already used somewhere in the
// config, this passes through — no point manufacturing extra false positives
// and pointless retries for the sake of this backstop check.
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
		"the user's description explicitly named more than one timeframe, but no module "+
			"in the config uses %s at all -- that piece (or pieces) of logic's timeframe got "+
			"dropped in translation, and it must not be allowed to silently follow the trigger "+
			"timeframe instead; go back to the user's own words and explicitly set the "+
			"corresponding module's timeframe field to the timeframe they actually named",
		strings.Join(missing, ", "))
}
