package main

import (
	"context"
	"log/slog"
	"time"

	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// promotionActor identifies the actor for automatic promotion checks,
// written into the actor field of the audit log.
const promotionActor = "paper-monitor"

// promotionStore is the minimal persistence interface needed to
// auto-promote paper-trading strategies.
//
// Using an interface instead of depending directly on *storage.Store lets
// checkPromotions's core logic be unit-tested without a real Postgres —
// storage.Store itself is only verified in integration tests that need a
// real database.
type promotionStore interface {
	ListStrategiesByState(ctx context.Context, userID string, state types.StrategyState) ([]types.StrategyConfig, error)
	ListStrategiesByStateAllUsers(ctx context.Context, state types.StrategyState) ([]types.StrategyConfig, error)
	PaperStats(ctx context.Context, userID, strategyID string) (strategy.PaperStats, error)
	UpdateStrategyState(ctx context.Context, userID string, t storage.Transition) error
}

// checkPromotions scans PAPER_TRADING strategies and promotes those whose
// paper-trading stats already qualify to LIVE_ELIGIBLE. A non-empty
// ownerUserID = single-tenant mode, scanning just that one user; empty =
// multi-tenant mode, scanning across all users — note that this scan scope
// is independent of this executor instance's own -state flag: even if this
// process was started with -state LIVE (dedicated to executing LIVE
// strategies), promotion checks still run in the background regardless —
// that's pre-existing behavior, not something this refactor introduced, so
// this can't reuse the strategy list main.go already loaded by -state
// (state is quite possibly not PAPER_TRADING at all); it must query
// independently.
//
// This step is safe for the system to do automatically: in the state
// machine rules, only LIVE_ELIGIBLE -> LIVE requires an explicit user
// nod — PAPER_TRADING -> LIVE_ELIGIBLE is purely "the data speaks for
// itself," no real money involved.
//
// A stats-read failure or promotion failure for one strategy must not drag
// down other strategies — this isolation principle runs through the whole
// execution layer, and applies here too: a data problem with one strategy
// shouldn't block other strategies that have already run long enough. In
// multi-tenant mode this isolation naturally extends to "other users'
// strategies in the same batch are unaffected" too, with no extra code
// needed.
func checkPromotions(ctx context.Context, store promotionStore, ownerUserID string, gate strategy.Gate, logger *slog.Logger) {
	var strategies []types.StrategyConfig
	var err error
	if ownerUserID != "" {
		strategies, err = store.ListStrategiesByState(ctx, ownerUserID, types.StatePaperTrading)
	} else {
		strategies, err = store.ListStrategiesByStateAllUsers(ctx, types.StatePaperTrading)
	}
	if err != nil {
		logger.Error("failed to scan paper-trading strategies", "err", err)
		return
	}

	for _, sc := range strategies {
		// Read sc.UserID directly from the queried row — in multi-tenant
		// mode each strategy belongs to a different user, so a single
		// ownerUserID fixed in a closure won't do.
		paper, err := store.PaperStats(ctx, sc.UserID, sc.ID)
		if err != nil {
			logger.Error("failed to read paper-trading stats, skipping this check", "strategy_id", sc.ID, "err", err)
			continue
		}

		evidence, err := strategy.CheckTransition(strategy.TransitionRequest{
			From: types.StatePaperTrading, To: types.StateLiveEligible,
			Actor: strategy.ActorSystem, ActorID: promotionActor,
			Reason: "paper-trading stats meet the bar, auto-promoted",
			Paper:  &paper,
		}, gate)
		if err != nil {
			logger.Debug("paper-trading gate not yet met", "strategy_id", sc.ID,
				"paper_duration", paper.Duration().String(), "paper_trades", paper.TradeCount, "reason", err)
			continue
		}

		if err := store.UpdateStrategyState(ctx, sc.UserID, storage.Transition{
			StrategyID: sc.ID, From: types.StatePaperTrading, To: types.StateLiveEligible,
			Actor:  string(strategy.ActorSystem) + ":" + promotionActor,
			Reason: "paper-trading stats meet the bar, auto-promoted", Evidence: evidence,
		}); err != nil {
			logger.Error("failed to promote to LIVE_ELIGIBLE", "strategy_id", sc.ID, "err", err)
			continue
		}

		logger.Info("strategy promoted to LIVE_ELIGIBLE",
			"strategy_id", sc.ID, "symbol", sc.Symbol,
			"paper_duration", paper.Duration().String(), "paper_trades", paper.TradeCount)
	}
}

// runPromotionLoop calls checkPromotions repeatedly at a fixed interval
// until ctx is cancelled.
//
// It checks once immediately on startup: no need to wait out the first
// interval — if a strategy happened to qualify right before the executor
// restarted, it shouldn't have to wait out a whole extra cycle for nothing.
func runPromotionLoop(ctx context.Context, store promotionStore, ownerUserID string, gate strategy.Gate, logger *slog.Logger, interval time.Duration) {
	checkPromotions(ctx, store, ownerUserID, gate, logger)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkPromotions(ctx, store, ownerUserID, gate, logger)
		}
	}
}
