package webui

import (
	"context"
	"fmt"
	"strings"

	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// errNotFound 让假实现能复现 storage.ErrNotFound 的语义，不必依赖真实 Postgres。
var errNotFound = storage.ErrNotFound

// fakeStore 是 Store 接口的内存实现，供 handler 测试使用——不需要真实 Postgres。
// 直接照抄 cmd/executor 的 fakePromotionStore 模式。
//
// 多用户 SaaS 改造：strategies/profiles/brokers 继续按实体 ID 存（不是按用户分层存），
// 归属信息就存在每个值自己的 UserID 字段里，跟真实 Postgres 用一列做过滤是同一个
// 语义——每个 Get/List/Delete/Activate 假实现都要检查这个字段，不能假装"反正是内存里
// 的假数据，检不检查都行"，这正是要测的隔离属性本身。
type fakeStore struct {
	strategies    map[string]types.StrategyConfig
	saved         []types.StrategyConfig
	transitions   map[string][]storage.Transition
	decisions     map[string][]types.Decision
	orders        map[string][]types.Order
	backtests     map[string]types.BacktestResult
	paperStats    map[string]strategy.PaperStats
	profiles      map[string]storage.AgentProfile
	brokers       map[string]storage.BrokerProfile
	users         map[string]storage.User
	notifChannels map[string]storage.NotificationChannel

	saveErr             error
	recordTransitionErr error
	updateStateErr      error
	updateStateCalls    []storage.Transition
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		strategies:    map[string]types.StrategyConfig{},
		transitions:   map[string][]storage.Transition{},
		decisions:     map[string][]types.Decision{},
		orders:        map[string][]types.Order{},
		backtests:     map[string]types.BacktestResult{},
		paperStats:    map[string]strategy.PaperStats{},
		profiles:      map[string]storage.AgentProfile{},
		brokers:       map[string]storage.BrokerProfile{},
		users:         map[string]storage.User{},
		notifChannels: map[string]storage.NotificationChannel{},
	}
}

func (f *fakeStore) ListStrategies(_ context.Context, userID string) ([]types.StrategyConfig, error) {
	var out []types.StrategyConfig
	for _, sc := range f.strategies {
		if sc.UserID == userID {
			out = append(out, sc)
		}
	}
	return out, nil
}

func (f *fakeStore) GetStrategy(_ context.Context, userID, id string) (types.StrategyConfig, error) {
	sc, ok := f.strategies[id]
	if !ok || sc.UserID != userID {
		return types.StrategyConfig{}, errNotFound
	}
	return sc, nil
}

func (f *fakeStore) SaveStrategy(_ context.Context, cfg types.StrategyConfig) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	// 照抄真实 postgres.go 的 SaveStrategy：id 已存在但属于别的用户时拒绝，不能悄悄
	// 覆盖——这是复核时发现的真实漏洞的回归防护，假实现也要体现同一条规则，
	// 不然测试没法真正验证它。
	if existing, ok := f.strategies[cfg.ID]; ok && existing.UserID != cfg.UserID {
		return errNotFound
	}
	f.strategies[cfg.ID] = cfg
	f.saved = append(f.saved, cfg)
	return nil
}

func (f *fakeStore) DeleteStrategy(_ context.Context, userID, id string) error {
	sc, ok := f.strategies[id]
	if !ok || sc.UserID != userID {
		return errNotFound
	}
	delete(f.strategies, id)
	delete(f.transitions, id)
	delete(f.decisions, id)
	delete(f.orders, id)
	delete(f.backtests, id)
	delete(f.paperStats, id)
	return nil
}

func (f *fakeStore) RecordTransition(_ context.Context, t storage.Transition) error {
	if f.recordTransitionErr != nil {
		return f.recordTransitionErr
	}
	f.transitions[t.StrategyID] = append(f.transitions[t.StrategyID], t)
	return nil
}

func (f *fakeStore) UpdateStrategyState(_ context.Context, userID string, t storage.Transition) error {
	f.updateStateCalls = append(f.updateStateCalls, t)
	if f.updateStateErr != nil {
		return f.updateStateErr
	}
	sc, ok := f.strategies[t.StrategyID]
	if !ok || sc.UserID != userID {
		return errNotFound
	}
	sc.State = t.To
	f.strategies[t.StrategyID] = sc
	f.transitions[t.StrategyID] = append(f.transitions[t.StrategyID], t)
	return nil
}

func (f *fakeStore) ListTransitions(_ context.Context, userID, strategyID string) ([]storage.Transition, error) {
	if sc, ok := f.strategies[strategyID]; !ok || sc.UserID != userID {
		return nil, errNotFound
	}
	return f.transitions[strategyID], nil
}

func (f *fakeStore) ListDecisions(_ context.Context, userID, strategyID string, _ int) ([]types.Decision, error) {
	if sc, ok := f.strategies[strategyID]; !ok || sc.UserID != userID {
		return nil, errNotFound
	}
	return f.decisions[strategyID], nil
}

func (f *fakeStore) ListOrders(_ context.Context, userID, strategyID string, _ int) ([]types.Order, error) {
	if sc, ok := f.strategies[strategyID]; !ok || sc.UserID != userID {
		return nil, errNotFound
	}
	return f.orders[strategyID], nil
}

func (f *fakeStore) PaperStats(_ context.Context, userID, strategyID string) (strategy.PaperStats, error) {
	if sc, ok := f.strategies[strategyID]; !ok || sc.UserID != userID {
		return strategy.PaperStats{}, fmt.Errorf("策略 %s：%w", strategyID, errNotFound)
	}
	ps, ok := f.paperStats[strategyID]
	if !ok {
		return strategy.PaperStats{}, fmt.Errorf("策略 %s 从未进入过模拟盘：%w", strategyID, errNotFound)
	}
	return ps, nil
}

func (f *fakeStore) LatestBacktestResult(_ context.Context, userID, strategyID string) (types.BacktestResult, error) {
	if sc, ok := f.strategies[strategyID]; !ok || sc.UserID != userID {
		return types.BacktestResult{}, errNotFound
	}
	bt, ok := f.backtests[strategyID]
	if !ok {
		return types.BacktestResult{}, errNotFound
	}
	return bt, nil
}

func (f *fakeStore) SaveAgentProfile(_ context.Context, p storage.AgentProfile, activate bool) error {
	if activate {
		for id, existing := range f.profiles {
			if existing.UserID == p.UserID {
				existing.IsActive = false
				f.profiles[id] = existing
			}
		}
		p.IsActive = true
	}
	f.profiles[p.ID] = p
	return nil
}

func (f *fakeStore) GetAgentProfile(_ context.Context, userID, id string) (storage.AgentProfile, error) {
	p, ok := f.profiles[id]
	if !ok || p.UserID != userID {
		return storage.AgentProfile{}, fmt.Errorf("模型配置 %s：%w", id, errNotFound)
	}
	return p, nil
}

func (f *fakeStore) ListAgentProfiles(_ context.Context, userID string) ([]storage.AgentProfile, error) {
	var out []storage.AgentProfile
	for _, p := range f.profiles {
		if p.UserID == userID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) ActivateAgentProfile(_ context.Context, userID, id string) error {
	target, ok := f.profiles[id]
	if !ok || target.UserID != userID {
		return fmt.Errorf("模型配置 %s：%w", id, errNotFound)
	}
	for pid, existing := range f.profiles {
		if existing.UserID == userID {
			existing.IsActive = pid == id
			f.profiles[pid] = existing
		}
	}
	return nil
}

func (f *fakeStore) DeleteAgentProfile(_ context.Context, userID, id string) error {
	p, ok := f.profiles[id]
	if !ok || p.UserID != userID {
		return fmt.Errorf("模型配置 %s：%w", id, errNotFound)
	}
	delete(f.profiles, id)
	return nil
}

func (f *fakeStore) ActiveAgentProfile(_ context.Context, userID string) (storage.AgentProfile, error) {
	for _, p := range f.profiles {
		if p.UserID == userID && p.IsActive {
			return p, nil
		}
	}
	return storage.AgentProfile{}, fmt.Errorf("当前生效的模型配置：%w", errNotFound)
}

func (f *fakeStore) SaveBrokerProfile(_ context.Context, p storage.BrokerProfile, activate bool) error {
	if activate {
		for id, existing := range f.brokers {
			if existing.UserID == p.UserID && existing.Broker == p.Broker {
				existing.IsActive = false
				f.brokers[id] = existing
			}
		}
		p.IsActive = true
	}
	f.brokers[p.ID] = p
	return nil
}

func (f *fakeStore) GetBrokerProfile(_ context.Context, userID, id string) (storage.BrokerProfile, error) {
	p, ok := f.brokers[id]
	if !ok || p.UserID != userID {
		return storage.BrokerProfile{}, fmt.Errorf("交易所配置 %s：%w", id, errNotFound)
	}
	return p, nil
}

func (f *fakeStore) ListBrokerProfiles(_ context.Context, userID string) ([]storage.BrokerProfile, error) {
	var out []storage.BrokerProfile
	for _, p := range f.brokers {
		if p.UserID == userID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) ActivateBrokerProfile(_ context.Context, userID, id string) error {
	target, ok := f.brokers[id]
	if !ok || target.UserID != userID {
		return fmt.Errorf("交易所配置 %s：%w", id, errNotFound)
	}
	for pid, existing := range f.brokers {
		if existing.UserID == userID && existing.Broker == target.Broker {
			existing.IsActive = pid == id
			f.brokers[pid] = existing
		}
	}
	return nil
}

func (f *fakeStore) DeleteBrokerProfile(_ context.Context, userID, id string) error {
	p, ok := f.brokers[id]
	if !ok || p.UserID != userID {
		return fmt.Errorf("交易所配置 %s：%w", id, errNotFound)
	}
	delete(f.brokers, id)
	return nil
}

func (f *fakeStore) ActiveBrokerProfile(_ context.Context, userID, broker string) (storage.BrokerProfile, error) {
	for _, p := range f.brokers {
		if p.UserID == userID && p.Broker == broker && p.IsActive {
			return p, nil
		}
	}
	return storage.BrokerProfile{}, fmt.Errorf("%s 当前生效的交易所配置：%w", broker, errNotFound)
}

// SaveNotificationChannel/ListNotificationChannels/GetNotificationChannel/
// SetNotificationChannelEnabled/DeleteNotificationChannel 是提醒渠道的假实现——
// 跟 broker 的假实现不同，这里没有"激活切换"逻辑：任意多行可以同时 IsEnabled=true，
// 见 storage.NotificationChannel 的注释，假实现要忠实复现这条语义，不能顺手抄
// broker 那套互斥逻辑。
func (f *fakeStore) SaveNotificationChannel(_ context.Context, c storage.NotificationChannel) error {
	f.notifChannels[c.ID] = c
	return nil
}

func (f *fakeStore) ListNotificationChannels(_ context.Context, userID string) ([]storage.NotificationChannel, error) {
	var out []storage.NotificationChannel
	for _, c := range f.notifChannels {
		if c.UserID == userID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) GetNotificationChannel(_ context.Context, userID, id string) (storage.NotificationChannel, error) {
	c, ok := f.notifChannels[id]
	if !ok || c.UserID != userID {
		return storage.NotificationChannel{}, fmt.Errorf("提醒渠道 %s：%w", id, errNotFound)
	}
	return c, nil
}

func (f *fakeStore) SetNotificationChannelEnabled(_ context.Context, userID, id string, enabled bool) error {
	c, ok := f.notifChannels[id]
	if !ok || c.UserID != userID {
		return fmt.Errorf("提醒渠道 %s：%w", id, errNotFound)
	}
	c.IsEnabled = enabled
	f.notifChannels[id] = c
	return nil
}

func (f *fakeStore) DeleteNotificationChannel(_ context.Context, userID, id string) error {
	c, ok := f.notifChannels[id]
	if !ok || c.UserID != userID {
		return fmt.Errorf("提醒渠道 %s：%w", id, errNotFound)
	}
	delete(f.notifChannels, id)
	return nil
}

func (f *fakeStore) CreateUser(_ context.Context, u storage.User) error {
	for _, existing := range f.users {
		if strings.EqualFold(existing.Email, u.Email) {
			return storage.ErrEmailTaken
		}
	}
	if u.PreferredLang == "" {
		u.PreferredLang = "zh" // mirrors storage.Store.CreateUser's own normalization
	}
	f.users[u.ID] = u
	return nil
}

func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (storage.User, error) {
	for _, u := range f.users {
		if strings.EqualFold(u.Email, email) {
			return u, nil
		}
	}
	return storage.User{}, fmt.Errorf("账号 %s：%w", email, errNotFound)
}

func (f *fakeStore) GetUser(_ context.Context, id string) (storage.User, error) {
	u, ok := f.users[id]
	if !ok {
		return storage.User{}, fmt.Errorf("账号 %s：%w", id, errNotFound)
	}
	return u, nil
}

func (f *fakeStore) SetUserPreferredLang(_ context.Context, userID, lang string) error {
	u, ok := f.users[userID]
	if !ok {
		return fmt.Errorf("账号 %s：%w", userID, errNotFound)
	}
	u.PreferredLang = lang
	f.users[userID] = u
	return nil
}
