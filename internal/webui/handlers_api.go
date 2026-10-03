package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tradeforge/internal/i18n"
	"tradeforge/internal/modules/fakeout"
	"tradeforge/internal/modules/poc"
	"tradeforge/internal/modules/supportresistance"
	"tradeforge/internal/modules/trendlinepullback"
	"tradeforge/pkg/types"
)

// apiCandle is the candle shape fed to the frontend chart library (lightweight-charts).
//
// Converting price/volume to float64 here is deliberate: this is pure
// display use (pixel positioning), never feeding into any monetary
// calculation or trading decision, so it doesn't conflict with the
// project's "no float64 for financial math" rule -- every place that
// actually places an order, computes a fee, or sizes a position goes
// through decimal.Decimal, none of which ever passes through this endpoint.
type apiCandle struct {
	Time   int64   `json:"time"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

// handleAPICandles provides read-only candle data to the visual strategy builder page.
//
// Queries OKX directly: this is a public, read-only endpoint that needs no
// API key, the same usage pattern as cmd/signal-engine's. It serves only the
// builder's "look at the chart, place modules" purpose, not a general market
// data endpoint.
func (s *Server) handleAPICandles(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(r.PathValue("symbol"))
	if !looksLikeSymbol(symbol) {
		http.NotFound(w, r)
		return
	}

	lang := resolveLang(r)
	tf, limit, err := parseTimeframeAndLimit(r, lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var candles []types.Candle
	// before: when the builder chart is dragged left to the earliest edge of
	// already-loaded data, the frontend sends the earliest currently-loaded
	// candle's time to keep paging backward. Left blank, it falls back to
	// the original "most recent limit candles" path, unaffected for any
	// existing caller.
	if raw := r.URL.Query().Get("before"); raw != "" {
		beforeSec, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil {
			http.Error(w, i18n.Render(lang, types.Msg("webui.api.before_must_be_unix_seconds")), http.StatusBadRequest)
			return
		}
		candles, err = s.okxClient.FetchCandlesBefore(r.Context(), symbol, tf, limit, time.Unix(beforeSec, 0))
	} else {
		candles, err = s.okxClient.FetchCandles(r.Context(), symbol, tf, limit)
	}
	if err != nil {
		s.serverError(w, r, err)
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

// symbolSearchDefaultLimit/symbolSearchMaxLimit bound how many suggestions
// GET /api/symbols returns at once: the default is just enough to fill a
// dropdown, and the limit param lets a caller ask for more, but capped to
// keep a malicious or mistaken oversized value from dumping the whole
// cached list back.
const symbolSearchDefaultLimit = 12
const symbolSearchMaxLimit = 50

// handleAPISymbolSearch provides autocomplete suggestions for the "enter a
// symbol" search box: matches the user's typed substring against symbols
// that genuinely exist on OKX spot (symbolCache, see symbols.go), not a
// local guessed fixed table -- this guarantees a suggested symbol will
// always have data on /api/candles, avoiding the gap where picking a
// suggestion leads to a later "can't infer an OKX instId" error.
func (s *Server) handleAPISymbolSearch(w http.ResponseWriter, r *http.Request) {
	limit := symbolSearchDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= symbolSearchMaxLimit {
			limit = n
		}
	}
	matches, err := s.symbolCache.search(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if matches == nil {
		matches = []string{}
	}
	writeJSON(w, matches)
}

func parseTimeframeAndLimit(r *http.Request, lang i18n.Lang) (types.Timeframe, int, error) {
	tf := types.Timeframe(r.URL.Query().Get("timeframe"))
	if tf == "" {
		tf = types.TF1h
	}
	if !tf.Valid() {
		return "", 0, errors.New(i18n.Render(lang, types.Msg("webui.api.unsupported_timeframe", "timeframes", timeframeList())))
	}

	limit := 300
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return "", 0, errors.New(i18n.Render(lang, types.Msg("webui.api.limit_must_be_positive_int")))
		}
		limit = n
	}
	return tf, limit, nil
}

// apiLevel is one key level returned by the support/resistance preview.
type apiLevel struct {
	Price   float64 `json:"price"`
	Kind    string  `json:"kind"`
	Touches int     `json:"touches"`
}

// windowStartUnix converts the module Raw map's "window_start" (an RFC3339
// string) into the Unix seconds lightweight-charts uses -- the same time
// representation as apiCandle.Time, so the builder can use it directly to
// position an X coordinate and draw a vertical line marking "this analysis
// used history starting from here." Returns 0 on a parse failure (the
// module didn't provide one, or the format is wrong); the caller must judge
// for itself whether to use the value.
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

// apiSupportResistancePreview is the data returned by the support/resistance
// preview. WindowStart is the history's starting point for this analysis
// (Unix seconds, the same representation as apiCandle.Time), letting the
// builder draw a vertical line marking "the system is looking at this
// span" -- given regardless of whether any key level was found; when none
// is found, the user can tell directly from this line that "the system
// looked over this whole span, it just didn't find one," rather than
// mistakenly thinking it wasn't looking at all.
type apiSupportResistancePreview struct {
	WindowStart int64      `json:"window_start,omitempty"`
	Levels      []apiLevel `json:"levels"`
}

// handleAPIPreviewSupportResistance runs the real support_resistance module
// with the builder's current parameters (not a JS reimplementation of the
// clustering algorithm), returning the detected key levels for the builder
// to draw as reference lines.
//
// support_resistance has no "user manually specifies a price" parameter
// (the user already confirmed accepting this limitation, before it was
// implemented), so what's given here is "what the module actually detects
// under this exact set of parameters" -- the same logic seen when the
// strategy actually runs, so the preview and the live behavior never
// diverge.
func (s *Server) handleAPIPreviewSupportResistance(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(r.PathValue("symbol"))
	if !looksLikeSymbol(symbol) {
		http.NotFound(w, r)
		return
	}
	lang := resolveLang(r)
	tf, limit, err := parseTimeframeAndLimit(r, lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	params, err := parseModuleParams(r, supportresistance.New().RequiredParams(), lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	candles, err := s.okxClient.FetchCandles(r.Context(), symbol, tf, limit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	sig, err := supportresistance.New().Evaluate(r.Context(),
		types.MarketData{Symbol: symbol, Timeframe: tf, Candles: candles}, params)
	if err != nil {
		http.Error(w, i18n.Render(lang, types.Msg("webui.api.invalid_params", "error", err.Error())), http.StatusBadRequest)
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

// apiFakeoutPreview is the data returned by the fakeout preview: when
// range_found is true, it gives the currently identified consolidation
// range's high/low (given regardless of whether a fakeout actually
// triggered -- the builder uses it to draw the routine reference lines for
// "which range the system is watching," just one high and one low, not a
// pile of small key levels); when event is not "none," the latest candle
// genuinely triggered a fakeout, with which side broke, the breakout price,
// and the reclaim price -- the builder uses this to draw one extra, more
// prominent line, a direct, visual answer to "did the system really
// understand the fakeout the user described."
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

// handleAPIPreviewFakeout runs the real fakeout module (not a
// reimplementation in JS), returning this analysis's identified
// consolidation range and whether a fakeout was genuinely detected right now.
func (s *Server) handleAPIPreviewFakeout(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(r.PathValue("symbol"))
	if !looksLikeSymbol(symbol) {
		http.NotFound(w, r)
		return
	}
	lang := resolveLang(r)
	tf, limit, err := parseTimeframeAndLimit(r, lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	fm := fakeout.New()
	params, err := parseModuleParams(r, fm.RequiredParams(), lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	candles, err := s.okxClient.FetchCandles(r.Context(), symbol, tf, limit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	sig, err := fm.Evaluate(r.Context(),
		types.MarketData{Symbol: symbol, Timeframe: tf, Candles: candles}, params)
	if err != nil {
		http.Error(w, i18n.Render(lang, types.Msg("webui.api.invalid_params", "error", err.Error())), http.StatusBadRequest)
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

// apiPOCPreview is the data returned by the POC preview. Available being
// false means it couldn't be computed (e.g. price barely moved within the
// lookback window), letting the builder decide not to draw a line, rather
// than drawing a misleading one at price zero.
type apiPOCPreview struct {
	Available     bool    `json:"available"`
	Price         float64 `json:"price,omitempty"`
	Volume        float64 `json:"volume,omitempty"`
	IsApproximate bool    `json:"is_approximate,omitempty"`
	WindowStart   int64   `json:"window_start,omitempty"`
}

// handleAPIPreviewPOC runs the real poc module, returning the volume point
// of control computed under the current parameters.
func (s *Server) handleAPIPreviewPOC(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(r.PathValue("symbol"))
	if !looksLikeSymbol(symbol) {
		http.NotFound(w, r)
		return
	}
	lang := resolveLang(r)
	tf, limit, err := parseTimeframeAndLimit(r, lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pm := poc.New()
	params, err := parseModuleParams(r, pm.RequiredParams(), lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	candles, err := s.okxClient.FetchCandles(r.Context(), symbol, tf, limit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	sig, err := pm.Evaluate(r.Context(),
		types.MarketData{Symbol: symbol, Timeframe: tf, Candles: candles}, params)
	if err != nil {
		http.Error(w, i18n.Render(lang, types.Msg("webui.api.invalid_params", "error", err.Error())), http.StatusBadRequest)
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

// apiTrendlinePullbackPivot is one point on the chart (a swing point, or the
// pullback's own tested extreme), in the same time/price representation as
// apiCandle so the frontend can position it directly.
type apiTrendlinePullbackPivot struct {
	Time  int64   `json:"time"`
	Price float64 `json:"price"`
}

// apiTrendlinePullbackPreview is the data returned by the trendline_pullback
// preview. Pattern is empty when no qualifying pattern was found right now
// (either direction) -- the builder then draws nothing, the same "don't draw
// a misleading line" judgment as apiPOCPreview.Available. When non-empty,
// TrendlineStart/TrendlineEnd are the two confirmed swing points the
// trendline connects; TrendlineAtTest is that same trendline extrapolated to
// TestedPoint's candle, so the builder can draw one straight line through
// all three of TrendlineStart/TrendlineEnd/TrendlineAtTest (exactly
// collinear by construction). TestedPoint is the pullback/rally's own actual
// extreme that was checked against the trendline -- deliberately a separate
// point rather than reused for the line's end, since its price is real
// candle data, not the trendline's value; the small gap between
// TrendlineAtTest and TestedPoint at the same point in time is exactly what
// trendline_tolerance bounds, and is worth being able to see on the chart,
// not papered over by snapping the line onto the candle.
type apiTrendlinePullbackPreview struct {
	Pattern         string                     `json:"pattern,omitempty"`
	Direction       string                     `json:"direction,omitempty"`
	TrendlineStart  *apiTrendlinePullbackPivot `json:"trendline_start,omitempty"`
	TrendlineEnd    *apiTrendlinePullbackPivot `json:"trendline_end,omitempty"`
	TrendlineAtTest *apiTrendlinePullbackPivot `json:"trendline_at_test,omitempty"`
	TestedPoint     *apiTrendlinePullbackPivot `json:"tested_point,omitempty"`
}

// handleAPIPreviewTrendlinePullback runs the real trendline_pullback module,
// returning the swing structure and tested point for the builder to draw as
// a single extended trendline, when a pattern is currently confirmed.
func (s *Server) handleAPIPreviewTrendlinePullback(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(r.PathValue("symbol"))
	if !looksLikeSymbol(symbol) {
		http.NotFound(w, r)
		return
	}
	lang := resolveLang(r)
	tf, limit, err := parseTimeframeAndLimit(r, lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	tm := trendlinepullback.New()
	params, err := parseModuleParams(r, tm.RequiredParams(), lang)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	candles, err := s.okxClient.FetchCandles(r.Context(), symbol, tf, limit)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	sig, err := tm.Evaluate(r.Context(),
		types.MarketData{Symbol: symbol, Timeframe: tf, Candles: candles}, params)
	if err != nil {
		http.Error(w, i18n.Render(lang, types.Msg("webui.api.invalid_params", "error", err.Error())), http.StatusBadRequest)
		return
	}

	pattern, _ := sig.Raw["pattern"].(string)
	if pattern == "" {
		writeJSON(w, apiTrendlinePullbackPreview{})
		return
	}

	startKey, endKey, testKey := "trend_low_prev", "trend_low_last", "tested_low"
	if pattern == "downtrend_rally" {
		startKey, endKey, testKey = "trend_high_prev", "trend_high_last", "tested_high"
	}
	testedPoint := trendlinePullbackPivotFromRaw(sig.Raw[testKey], candles)
	var trendlineAtTest *apiTrendlinePullbackPivot
	if testedPoint != nil {
		if priceStr, ok := sig.Raw["trendline_price_at_test"].(string); ok {
			if price, err := strconv.ParseFloat(priceStr, 64); err == nil {
				trendlineAtTest = &apiTrendlinePullbackPivot{Time: testedPoint.Time, Price: price}
			}
		}
	}
	writeJSON(w, apiTrendlinePullbackPreview{
		Pattern:         pattern,
		Direction:       string(sig.Direction),
		TrendlineStart:  trendlinePullbackPivotFromRaw(sig.Raw[startKey], candles),
		TrendlineEnd:    trendlinePullbackPivotFromRaw(sig.Raw[endKey], candles),
		TrendlineAtTest: trendlineAtTest,
		TestedPoint:     testedPoint,
	})
}

// trendlinePullbackPivotFromRaw resolves one of trendlinepullback's Raw
// pivot entries (shape {"index": int, "price": string}) into a chart-ready
// point, looking the index up against the exact candle slice that was
// passed into Evaluate so Time lines up with apiCandle.Time.
func trendlinePullbackPivotFromRaw(v any, candles []types.Candle) *apiTrendlinePullbackPivot {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	idx, ok := m["index"].(int)
	if !ok || idx < 0 || idx >= len(candles) {
		return nil
	}
	priceStr, _ := m["price"].(string)
	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		return nil
	}
	return &apiTrendlinePullbackPivot{Time: candles[idx].OpenTime.Unix(), Price: price}
}

// parseModuleParams parses out the query params in the request that share a
// name with one of a module's ParamSpecs; a missing one is simply skipped
// (Evaluate's internal ResolveParams fills in the default).
func parseModuleParams(r *http.Request, specs []types.ParamSpec, lang i18n.Lang) (map[string]any, error) {
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
				return nil, errors.New(i18n.Render(lang, types.Msg("webui.api.param_must_be_int", "name", spec.Name, "value", raw)))
			}
			params[spec.Name] = n
		case types.ParamFloat:
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, errors.New(i18n.Render(lang, types.Msg("webui.api.param_must_be_number", "name", spec.Name, "value", raw)))
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

// apiModule is one entry in the module catalog; its fields correspond
// roughly one-to-one with ParamSpec (pkg/types/params.go, the single source
// of truth for "a module explaining itself"), letting the frontend generate
// a generic parameter form without maintaining a separate form definition
// per module -- the one difference is the Description field: ParamSpec.
// Description is now a translatable types.Message, which can't be handed
// to the frontend as-is (the frontend has no catalog to look it up in), so
// it's rendered into a ready-made string via i18n.Render in the request's
// language (resolveLang) before being sent.
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

// handleAPIModules lists every registered module, for the visual builder's "add module" selector.
func (s *Server) handleAPIModules(w http.ResponseWriter, r *http.Request) {
	lang := resolveLang(r)
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
