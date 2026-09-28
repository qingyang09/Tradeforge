package engine

import (
	"fmt"
	"sort"
	"strings"

	"tradeforge/pkg/types"
)

// aggregate combines a set of module signals into a final decision according
// to the configured combination logic.
//
// Both modes share the same safety bias: when in doubt, don't trigger.
// Degraded signals are always treated as neutral — better to miss an
// opportunity than to place an order on incomplete information.
func aggregate(cfg types.StrategyConfig, signals []types.Signal) (types.Direction, float64, bool, string) {
	switch cfg.Combine {
	case types.CombineAll:
		return aggregateAll(signals)
	case types.CombineWeighted:
		return aggregateWeighted(cfg, signals)
	default:
		// Validate should already have rejected an illegal combine mode;
		// reaching here means validation was bypassed somehow.
		return types.DirectionNeutral, 0, false,
			fmt.Sprintf("unknown combine mode %q, not triggering", cfg.Combine)
	}
}

// aggregateAll requires every module to emit a non-neutral signal in the
// same direction.
//
// Any module that's neutral, degraded, or pointing the opposite way blocks
// the trigger outright — that's exactly what choosing ALL means to the
// user: "only act when every condition is satisfied."
func aggregateAll(signals []types.Signal) (types.Direction, float64, bool, string) {
	if len(signals) == 0 {
		return types.DirectionNeutral, 0, false, "no module signals"
	}

	var blockers []string
	dir := types.DirectionNeutral
	sum := 0.0

	for _, s := range signals {
		switch {
		case s.Degraded:
			blockers = append(blockers, fmt.Sprintf("%s is degraded (%s)", s.Module, s.Err))
		case s.Direction == types.DirectionNeutral:
			blockers = append(blockers, fmt.Sprintf("%s is neutral", s.Module))
		case dir == types.DirectionNeutral:
			dir = s.Direction
		case s.Direction != dir:
			// NOTE: kept in Chinese — engine_test.go asserts on the "相反"
			// substring in this message (TestAggregateAllBlockedByOpposingModule).
			blockers = append(blockers,
				fmt.Sprintf("%s 方向为 %s，与其余模块的 %s 相反", s.Module, s.Direction, dir))
		}
		sum += s.Confidence
	}

	score := sum / float64(len(signals))

	if len(blockers) > 0 {
		return types.DirectionNeutral, score, false,
			fmt.Sprintf("ALL combine requires all %d modules to agree, but: %s",
				len(signals), strings.Join(blockers, "; "))
	}
	return dir, score, true,
		fmt.Sprintf("ALL combine: all %d modules gave a %s signal, average confidence %.3f",
			len(signals), dir, score)
}

// aggregateWeighted computes the weighted net directional strength.
//
// Net strength = (Σ long weight×confidence − Σ short weight×confidence) / Σ weight,
// landing in [-1, 1]. The denominator uses the sum of ALL module weights
// (including neutral and degraded ones), so "half the modules going silent"
// genuinely drags the strength down, instead of letting the remaining
// minority of modules push the score past the threshold on their own.
func aggregateWeighted(cfg types.StrategyConfig, signals []types.Signal) (types.Direction, float64, bool, string) {
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
		return types.DirectionNeutral, 0, false, "sum of all module weights is 0, cannot weight"
	}

	score := net / totalWeight
	abs := score
	dir := types.DirectionLong
	if score < 0 {
		abs, dir = -score, types.DirectionShort
	}

	note := ""
	if len(degraded) > 0 {
		sort.Strings(degraded)
		note = fmt.Sprintf(" (%s degraded, counted as neutral in the denominator)", strings.Join(degraded, ", "))
	}

	if abs < cfg.Threshold {
		return types.DirectionNeutral, score, false,
			fmt.Sprintf("WEIGHTED combine: weighted net strength %.3f, below threshold %.3f%s", score, cfg.Threshold, note)
	}
	return dir, score, true,
		fmt.Sprintf("WEIGHTED combine: weighted net strength %.3f (%s direction), meets threshold %.3f%s",
			score, dir, cfg.Threshold, note)
}
