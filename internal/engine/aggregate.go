package engine

import (
	"fmt"
	"sort"
	"strings"

	"tradeforge/pkg/types"
)

// aggregate 把一组模块信号按配置的组合逻辑聚合成最终决策。
//
// 两种模式共同的安全取向：拿不准就不触发。降级信号一律按中性处理，
// 宁可错过一次机会，也不要基于残缺的信息下单。
func aggregate(cfg types.StrategyConfig, signals []types.Signal) (types.Direction, float64, bool, string) {
	switch cfg.Combine {
	case types.CombineAll:
		return aggregateAll(signals)
	case types.CombineWeighted:
		return aggregateWeighted(cfg, signals)
	default:
		// Validate 应当已经拦下非法组合逻辑，走到这里说明校验被绕过了。
		return types.DirectionNeutral, 0, false,
			fmt.Sprintf("未知的组合逻辑 %q，不触发", cfg.Combine)
	}
}

// aggregateAll 要求全部模块给出同方向的非中性信号。
//
// 任何一个模块中性、降级或反向，都直接判定不触发——这正是用户选择 ALL 的本意：
// "所有条件都满足我才动手"。
func aggregateAll(signals []types.Signal) (types.Direction, float64, bool, string) {
	if len(signals) == 0 {
		return types.DirectionNeutral, 0, false, "没有任何模块信号"
	}

	var blockers []string
	dir := types.DirectionNeutral
	sum := 0.0

	for _, s := range signals {
		switch {
		case s.Degraded:
			blockers = append(blockers, fmt.Sprintf("%s 已降级（%s）", s.Module, s.Err))
		case s.Direction == types.DirectionNeutral:
			blockers = append(blockers, fmt.Sprintf("%s 为中性", s.Module))
		case dir == types.DirectionNeutral:
			dir = s.Direction
		case s.Direction != dir:
			blockers = append(blockers,
				fmt.Sprintf("%s 方向为 %s，与其余模块的 %s 相反", s.Module, s.Direction, dir))
		}
		sum += s.Confidence
	}

	score := sum / float64(len(signals))

	if len(blockers) > 0 {
		return types.DirectionNeutral, score, false,
			fmt.Sprintf("ALL 组合要求全部 %d 个模块同向，但：%s",
				len(signals), strings.Join(blockers, "；"))
	}
	return dir, score, true,
		fmt.Sprintf("ALL 组合：全部 %d 个模块均给出 %s 信号，平均置信度 %.3f",
			len(signals), dir, score)
}

// aggregateWeighted 计算加权净方向强度。
//
// 净强度 = (Σ 多头权重×置信度 − Σ 空头权重×置信度) / Σ 权重，落在 [-1, 1]。
// 分母用全部模块的权重之和（含中性与降级的），这样"一半模块沉默"会如实压低强度，
// 而不是让剩下的少数模块独自把分数顶到阈值以上。
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
		return types.DirectionNeutral, 0, false, "全部模块权重之和为 0，无法加权"
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
		note = fmt.Sprintf("（%s 已降级，按中性计入分母）", strings.Join(degraded, "、"))
	}

	if abs < cfg.Threshold {
		return types.DirectionNeutral, score, false,
			fmt.Sprintf("WEIGHTED 组合：加权净强度 %.3f，未达到阈值 %.3f%s", score, cfg.Threshold, note)
	}
	return dir, score, true,
		fmt.Sprintf("WEIGHTED 组合：加权净强度 %.3f（%s 方向），达到阈值 %.3f%s",
			score, dir, cfg.Threshold, note)
}
