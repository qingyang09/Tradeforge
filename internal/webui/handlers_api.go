package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tradeforge/internal/i18n"
	"tradeforge/internal/modules/fakeout"
	"tradeforge/internal/modules/poc"
	"tradeforge/internal/modules/supportresistance"
	"tradeforge/pkg/types"
)

// apiCandle 是喂给前端图表库（lightweight-charts）的蜡烛线形状。
//
// 价格/成交量在这里转成 float64 是刻意的：这是纯展示用途（像素定位），不参与任何
// 金额计算或交易决策，跟项目"金融计算禁止用 float64"的铁律不冲突——真正下单、算
// 手续费、算仓位的地方全部走 decimal.Decimal，一步都没有经过这个接口。
type apiCandle struct {
	Time   int64   `json:"time"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

// handleAPICandles 给可视化建策的画板页提供只读的 K 线数据。
//
// 直接查 OKX：这是公开只读接口，不需要 API key，跟 cmd/signal-engine 的用法一致。
// 只服务画板的"看图摆模块"这个用途，不是行情源的通用出口。
func (s *Server) handleAPICandles(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(r.PathValue("symbol"))
	if !looksLikeSymbol(symbol) {
		http.NotFound(w, r)
		return
	}

	tf, limit, err := parseTimeframeAndLimit(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var candles []types.Candle
	// before：画板图表左拖到已加载数据的最左端时，前端带上当前最早一根K线的时间
	// 继续往回翻页。留空走原来的"最近 limit 根"这条路径，不影响任何既有调用方。
	if raw := r.URL.Query().Get("before"); raw != "" {
		beforeSec, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil {
			http.Error(w, "before 必须是 Unix 秒时间戳", http.StatusBadRequest)
			return
		}
		candles, err = s.okxClient.FetchCandlesBefore(r.Context(), symbol, tf, limit, time.Unix(beforeSec, 0))
	} else {
		candles, err = s.okxClient.FetchCandles(r.Context(), symbol, tf, limit)
	}
	if err != nil {
		s.serverError(w, err)
		return
	}

	out := make([]apiCandle, len(candles))
	for i, c := range candles {
		out[i] = apiCandle{
			Time:   c.OpenTime.Unix(),
			Open:   mustFloat(c.Open),
			High:   mustFloat(c.High),
			Low:    mustFloat(c.Low),
			Close:  mustFloat(c.Close),
			Volume: mustFloat(c.Volume),
		}
	}
	writeJSON(w, out)
}

// symbolSearchDefaultLimit/symbolSearchMaxLimit 界定 GET /api/symbols 一次返回多少条
// 建议：默认给一个下拉框刚好装得下的数量，limit 参数允许调用方要更多，但设上限防止
// 恶意/失误传入一个夸张的值把整份缓存列表都吐回去。
const symbolSearchDefaultLimit = 12
const symbolSearchMaxLimit = 50

// handleAPISymbolSearch 给"输入标的"的搜索框提供自动补全建议：按用户输入的子串
// 匹配 OKX 真实存在的现货标的（symbolCache，见 symbols.go），不是本地瞎猜的固定表——
// 这样建议出来的标的一定能在 /api/candles 上查到数据，不会出现选了建议、下一步又
// 报"无法推断 OKX instId"的落差。
func (s *Server) handleAPISymbolSearch(w http.ResponseWriter, r *http.Request) {
	limit := symbolSearchDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= symbolSearchMaxLimit {
			limit = n
		}
	}
	matches, err := s.symbolCache.search(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if matches == nil {
		matches = []string{}
	}
	writeJSON(w, matches)
}

func parseTimeframeAndLimit(r *http.Request) (types.Timeframe, int, error) {
	tf := types.Timeframe(r.URL.Query().Get("timeframe"))
	if tf == "" {
		tf = types.TF1h
	}
	if !tf.Valid() {
		return "", 0, fmt.Errorf("不支持的周期，可选值为 %s", timeframeList())
	}

	limit := 300
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return "", 0, errors.New("limit 必须是正整数")
		}
		limit = n
	}
	return tf, limit, nil
}

// apiLevel 是支撑/阻力位预览返回的一条关键位。
type apiLevel struct {
	Price   float64 `json:"price"`
	Kind    string  `json:"kind"`
	Touches int     `json:"touches"`
}

// windowStartUnix 把模块 Raw 里的 "window_start"（RFC3339 字符串）转成
// lightweight-charts 用的 Unix 秒——跟 apiCandle.Time 是同一种时间表示，画板才能
// 直接拿它去定位 X 坐标画一条竖线，标出"这次分析用了从这里开始的历史数据"。
// 解析失败（模块没给、或格式不对）时返回 0，调用方要自己判断是否要用这个值。
func windowStartUnix(raw map[string]any) int64 {
	s, _ := raw["window_start"].(string)
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// apiSupportResistancePreview 是支撑/阻力位预览返回的数据。WindowStart 是本次分析
// 用到的历史数据起点（Unix 秒，跟 apiCandle.Time 同一种表示），供画板在图上画一条
// 竖线标出"系统正在看这一段历史"——不管有没有找到关键位都会给，找不到时用户能
// 从这条线上直接看出"系统看了这么长一段，只是没找到"，而不是误以为它什么都没看。
type apiSupportResistancePreview struct {
	WindowStart int64      `json:"window_start,omitempty"`
	Levels      []apiLevel `json:"levels"`
}

// handleAPIPreviewSupportResistance 用当前画板上的参数，跑一遍真实的
// support_resistance 模块（不是在 JS 里重新实现一遍聚类算法），返回检测到的关键位，
// 供画板把它们画成参考线。
//
// support_resistance 没有"用户手动指定价位"这个参数（用户已确认接受这个限制，
// 见实现前的确认），所以这里给的是"当前这组参数下，模块实际会检测到什么"——
// 跟真正跑策略时看到的是同一份逻辑，不会出现预览和实盘对不上的情况。
func (s *Server) handleAPIPreviewSupportResistance(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(r.PathValue("symbol"))
	if !looksLikeSymbol(symbol) {
		http.NotFound(w, r)
		return
	}
	tf, limit, err := parseTimeframeAndLimit(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	params, err := parseModuleParams(r, supportresistance.New().RequiredParams())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	candles, err := s.okxClient.FetchCandles(r.Context(), symbol, tf, limit)
	if err != nil {
		s.serverError(w, err)
		return
	}

	sig, err := supportresistance.New().Evaluate(r.Context(),
		types.MarketData{Symbol: symbol, Timeframe: tf, Candles: candles}, params)
	if err != nil {
		http.Error(w, "参数非法："+err.Error(), http.StatusBadRequest)
		return
	}

	levels, _ := sig.Raw["levels"].([]supportresistance.Level)
	out := apiSupportResistancePreview{
		WindowStart: windowStartUnix(sig.Raw),
		Levels:      make([]apiLevel, len(levels)),
	}
	for i, lv := range levels {
		out.Levels[i] = apiLevel{Price: mustFloat(lv.Price), Kind: lv.Kind, Touches: lv.Touches}
	}
	writeJSON(w, out)
}

// apiFakeoutPreview 是假突破预览返回的数据：range_found 为 true 时给出当前识别出的
// 盘整区间高/低点（不论有没有触发假突破都会给出，画板拿它画"系统正在盯着哪个区间"
// 的常规参考线，只有一高一低两条，不是一堆细碎关键位）；event 非 "none" 时表示当前
// 最新这根 K 线上真的检测到了假突破，附带突破的是哪一侧、突破价、收回价——画板据此
// 单独画一条更醒目的线，直观回答"系统是不是真的理解了用户说的假突破"这个问题。
type apiFakeoutPreview struct {
	RangeFound    bool    `json:"range_found"`
	RangeHigh     float64 `json:"range_high,omitempty"`
	RangeLow      float64 `json:"range_low,omitempty"`
	RangeBars     int     `json:"range_bars,omitempty"`
	WindowStart   int64   `json:"window_start,omitempty"`
	Event         string  `json:"event"`
	Direction     string  `json:"direction,omitempty"`
	LevelPrice    float64 `json:"level_price,omitempty"`
	BreakoutClose float64 `json:"breakout_close,omitempty"`
	ReclaimClose  float64 `json:"reclaim_close,omitempty"`
}

// handleAPIPreviewFakeout 跑一遍真实的 fakeout 模块（不是在 JS 里重新实现一遍），
// 返回本次识别出的盘整区间，以及当前是否真的检测到了假突破。
func (s *Server) handleAPIPreviewFakeout(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(r.PathValue("symbol"))
	if !looksLikeSymbol(symbol) {
		http.NotFound(w, r)
		return
	}
	tf, limit, err := parseTimeframeAndLimit(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	fm := fakeout.New()
	params, err := parseModuleParams(r, fm.RequiredParams())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	candles, err := s.okxClient.FetchCandles(r.Context(), symbol, tf, limit)
	if err != nil {
		s.serverError(w, err)
		return
	}

	sig, err := fm.Evaluate(r.Context(),
		types.MarketData{Symbol: symbol, Timeframe: tf, Candles: candles}, params)
	if err != nil {
		http.Error(w, "参数非法："+err.Error(), http.StatusBadRequest)
		return
	}

	out := apiFakeoutPreview{WindowStart: windowStartUnix(sig.Raw)}
	if found, _ := sig.Raw["range_found"].(bool); found {
		out.RangeFound = true
		if hiStr, ok := sig.Raw["range_high"].(string); ok {
			out.RangeHigh, _ = strconv.ParseFloat(hiStr, 64)
		}
		if loStr, ok := sig.Raw["range_low"].(string); ok {
			out.RangeLow, _ = strconv.ParseFloat(loStr, 64)
		}
		if bars, ok := sig.Raw["range_bars"].(int); ok {
			out.RangeBars = bars
		}
	}
	if event, _ := sig.Raw["event"].(string); event != "" {
		out.Event = event
	}
	if out.Event != "" && out.Event != "none" {
		out.Direction = string(sig.Direction)
		if priceStr, ok := sig.Raw["level_price"].(string); ok {
			out.LevelPrice, _ = strconv.ParseFloat(priceStr, 64)
		}
		if closeStr, ok := sig.Raw["breakout_close"].(string); ok {
			out.BreakoutClose, _ = strconv.ParseFloat(closeStr, 64)
		}
		if closeStr, ok := sig.Raw["reclaim_close"].(string); ok {
			out.ReclaimClose, _ = strconv.ParseFloat(closeStr, 64)
		}
	}
	writeJSON(w, out)
}

// apiPOCPreview 是 POC 预览返回的数据。Available 为 false 表示算不出来（比如回看
// 窗口内价格完全没有波动），画板据此决定要不要画线，而不是画一条价格为零的假线。
type apiPOCPreview struct {
	Available     bool    `json:"available"`
	Price         float64 `json:"price,omitempty"`
	Volume        float64 `json:"volume,omitempty"`
	IsApproximate bool    `json:"is_approximate,omitempty"`
	WindowStart   int64   `json:"window_start,omitempty"`
}

// handleAPIPreviewPOC 跑一遍真实的 poc 模块，返回当前参数下算出的成交量分布重心。
func (s *Server) handleAPIPreviewPOC(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(r.PathValue("symbol"))
	if !looksLikeSymbol(symbol) {
		http.NotFound(w, r)
		return
	}
	tf, limit, err := parseTimeframeAndLimit(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pm := poc.New()
	params, err := parseModuleParams(r, pm.RequiredParams())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	candles, err := s.okxClient.FetchCandles(r.Context(), symbol, tf, limit)
	if err != nil {
		s.serverError(w, err)
		return
	}

	sig, err := pm.Evaluate(r.Context(),
		types.MarketData{Symbol: symbol, Timeframe: tf, Candles: candles}, params)
	if err != nil {
		http.Error(w, "参数非法："+err.Error(), http.StatusBadRequest)
		return
	}

	windowStart := windowStartUnix(sig.Raw)
	priceStr, ok := sig.Raw["poc_price"].(string)
	if !ok || priceStr == "" {
		writeJSON(w, apiPOCPreview{Available: false, WindowStart: windowStart})
		return
	}
	price, _ := strconv.ParseFloat(priceStr, 64)
	var volume float64
	if volStr, ok := sig.Raw["poc_volume"].(string); ok {
		volume, _ = strconv.ParseFloat(volStr, 64)
	}
	writeJSON(w, apiPOCPreview{
		Available: true, Price: price, Volume: volume, IsApproximate: true, WindowStart: windowStart,
	})
}

// parseModuleParams 把请求里跟某个模块的 ParamSpec 同名的 query 参数解析出来，
// 缺省的直接跳过（Evaluate 内部的 ResolveParams 会补默认值）。
func parseModuleParams(r *http.Request, specs []types.ParamSpec) (map[string]any, error) {
	params := make(map[string]any, len(specs))
	for _, spec := range specs {
		raw := r.URL.Query().Get(spec.Name)
		if raw == "" {
			continue
		}
		switch spec.Type {
		case types.ParamInt:
			n, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("参数 %s 必须是整数：%q", spec.Name, raw)
			}
			params[spec.Name] = n
		case types.ParamFloat:
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, fmt.Errorf("参数 %s 必须是数字：%q", spec.Name, raw)
			}
			params[spec.Name] = f
		default:
			params[spec.Name] = raw
		}
	}
	return params, nil
}

func mustFloat(d interface{ Float64() (float64, bool) }) float64 {
	f, _ := d.Float64()
	return f
}

func timeframeList() string {
	parts := make([]string, len(types.SupportedTimeframes))
	for i, tf := range types.SupportedTimeframes {
		parts[i] = string(tf)
	}
	return strings.Join(parts, ", ")
}

// apiModule 是模块目录里的一条记录，字段跟 ParamSpec（pkg/types/params.go，
// "模块自解释"的唯一事实来源）基本一一对应，前端用它生成通用参数表单，不需要为
// 每个模块单独维护一份表单定义——区别只在于 Description 字段：ParamSpec.Description
// 现在是可翻译的 types.Message，不能直接原样吐给前端（前端没有目录可查），这里在
// 发送前先用 i18n.Render 渲染成一段现成文字。lang 目前固定用 i18n.DefaultLang
// （中文），跟这一轮其它 webui 改动一样，是给 Phase 4 接上按请求语言渲染留的位置。
type apiModule struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Params      []apiParamSpec `json:"params"`
}

// apiParamSpec mirrors types.ParamSpec for the JSON API, with Description
// pre-rendered to a plain string (see apiModule's doc comment).
type apiParamSpec struct {
	Name        string          `json:"name"`
	Type        types.ParamType `json:"type"`
	Description string          `json:"description"`
	Default     any             `json:"default,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Min         *float64        `json:"min,omitempty"`
	Max         *float64        `json:"max,omitempty"`
	Enum        []string        `json:"enum,omitempty"`
}

// handleAPIModules 列出全部已注册模块，供可视化建策的"添加模块"选择器使用。
func (s *Server) handleAPIModules(w http.ResponseWriter, r *http.Request) {
	// i18n.DefaultLang (Chinese) for now, matching this app's current
	// Chinese-only behavior exactly -- see render.go's msg template func doc
	// comment for why, and the plan's Phase 4 for the per-request fix.
	lang := i18n.DefaultLang
	all := s.registry.All()
	out := make([]apiModule, len(all))
	for i, m := range all {
		specs := m.RequiredParams()
		params := make([]apiParamSpec, len(specs))
		for j, p := range specs {
			params[j] = apiParamSpec{
				Name: p.Name, Type: p.Type, Description: i18n.Render(lang, p.Description),
				Default: p.Default, Required: p.Required, Min: p.Min, Max: p.Max, Enum: p.Enum,
			}
		}
		out[i] = apiModule{Name: m.Name(), Description: i18n.Render(lang, m.Description()), Params: params}
	}
	writeJSON(w, out)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
