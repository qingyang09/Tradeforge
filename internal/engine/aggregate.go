package engine

import (
	"fmt"
	"sort"
	"strings"

	"tradeforge/internal/i18n"
	"tradeforge/pkg/types"
)

// aggregate combines a set of module signals into a final decision according
// to the configured combination logic.
//
// Both modes share the same safety bias: when in doubt, don't trigger.
// Degraded signals are always treated as neutral — better to miss an
// opportunity than to place an order on incomplete information.
func aggregate(cfg types.StrategyConfig, signals []types.Signal) (types.Direction, float64, bool, types.Message) {
	switch cfg.Combine {
	case types.CombineAll:
		return aggregateAll(signals)
	case types.CombineWeighted:
		return aggregateWeighted(cfg, signals)
	default:
		// Validate should already have rejected an illegal combine mode;
		// reaching here means validation was bypassed somehow.
		return types.DirectionNeutral, 0, false,
			types.Msg("engine.decision.unknown_combine_mode", "mode", string(cfg.Combine))
	}
}

// blockerLang is the language individual per-module blocker fragments are
// rendered in before being joined into the outer Decision.Reason's "blockers"
// arg. This is a real, documented limitation (see the plan's Class B design):
// the outer sentence ("ALL requires every module to agree, but: {blockers}")
// stays fully translatable, but the embedded per-module detail is frozen in
// whatever language was in effect at compute time, same as every other
// interim i18n.DefaultLang spot this session -- a genuinely fully bilingual
// per-blocker breakdown would need Message to support a list of nested
// Messages, which is more machinery than this call site currently justifies.
const blockerLang = i18n.DefaultLang

// aggregateAll requires every module to emit a non-neutral signal in the
// same direction.
//
// Any module that's neutral, degraded, or pointing the opposite way blocks
// the trigger outright — that's exactly what choosing ALL means to the
// user: "only act when every condition is satisfied."
func aggregateAll(signals []types.Signal) (types.Direction, float64, bool, types.Message) {
	if len(signals) == 0 {
		return types.DirectionNeutral, 0, false, types.Msg("engine.decision.no_signals")
	}

	var blockers []string
	dir := types.DirectionNeutral
	sum := 0.0

	for _, s := range signals {
		switch {
		case s.Degraded:
			blockers = append(blockers, i18n.T(blockerLang, "engine.decision.blocker.degraded", "module", s.Module, "err", s.Err))
		case s.Direction == types.DirectionNeutral:
			blockers = append(blockers, i18n.T(blockerLang, "engine.decision.blocker.neutral", "module", s.Module))
		case dir == types.DirectionNeutral:
			dir = s.Direction
		case s.Direction != dir:
			// NOTE: the Chinese catalog entry for this key is kept in Chinese
			// and must keep containing "相反" -- engine_test.go asserts on
			// that substring (TestAggregateAllBlockedByOpposingModule).
			blockers = append(blockers, i18n.T(blockerLang, "engine.decision.blocker.opposing",
				"module", s.Module, "direction", string(s.Direction), "majority", string(dir)))
		}
		sum += s.Confidence
	}

	score := sum / float64(len(signals))

	if len(blockers) > 0 {
		return types.DirectionNeutral, score, false,
			types.Msg("engine.decision.all_blocked", "count", len(signals), "blockers", strings.Join(blockers, "; "))
	}
	return dir, score, true,
		types.Msg("engine.decision.all_triggered", "count", len(signals), "direction", string(dir), "score", fmt.Sprintf("%.3f", score))
}

// aggregateWeighted computes the weighted net directional strength.
//
// Net strength = (Σ long weight×confidence − Σ short weight×confidence) / Σ weight,
// landing in [-1, 1]. The denominator uses the sum of ALL module weights
// (including neutral and degraded ones), so "half the modules going silent"
// genuinely drags the strength down, instead of letting the remaining
// minority of modules push the score past the threshold on their own.
func aggregateWeighted(cfg types.StrategyConfig, signals []types.Signal) (types.Direction, float64, bool, types.Message) {
	weights := make(map[string]float64, len(cfg.Modules))
	for _, mc := range cfg.Modules {
		weights[mc.Module] = mc.Weight
	}

	var totalWeight, net float64
	var degraded []string
	for _, s := range signals {
		w := weights[s.Module]
		totalWeight += w
		if s.Degraded {
			degraded = append(degraded, s.Module)
			continue
		}
		switch s.Direction {
		case types.DirectionLong:
			net += w * s.Confidence
		case types.DirectionShort:
			net -= w * s.Confidence
		}
	}

	if totalWeight <= 0 {
		return types.DirectionNeutral, 0, false, types.Msg("engine.decision.zero_total_weight")
	}

	score := net / totalWeight
	abs := score
	dir := types.DirectionLong
	if score < 0 {
		abs, dir = -score, types.DirectionShort
	}

	degradedNote := ""
	if len(degraded) > 0 {
		sort.Strings(degraded)
		degradedNote = i18n.T(blockerLang, "engine.decision.degraded_note", "modules", strings.Join(degraded, ", "))
	}

	if abs < cfg.Threshold {
		return types.DirectionNeutral, score, false,
			types.Msg("engine.decision.weighted_below_threshold",
				"score", fmt.Sprintf("%.3f", score), "threshold", fmt.Sprintf("%.3f", cfg.Threshold), "note", degradedNote)
	}
	return dir, score, true,
		types.Msg("engine.decision.weighted_triggered",
			"score", fmt.Sprintf("%.3f", score), "direction", string(dir), "threshold", fmt.Sprintf("%.3f", cfg.Threshold), "note", degradedNote)
}
