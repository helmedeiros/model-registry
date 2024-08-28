package canary

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type PromDecider struct {
	client     *http.Client
	promURL    string
	threshold  float64
	window     time.Duration
	pollEvery  time.Duration
	minSamples int
	now        func() time.Time
}

type PromOpt func(*PromDecider)

func WithPromClient(c *http.Client) PromOpt        { return func(p *PromDecider) { p.client = c } }
func WithPromThreshold(t float64) PromOpt          { return func(p *PromDecider) { p.threshold = t } }
func WithPromWindow(w time.Duration) PromOpt       { return func(p *PromDecider) { p.window = w } }
func WithPromPollEvery(d time.Duration) PromOpt    { return func(p *PromDecider) { p.pollEvery = d } }
func WithPromMinSamples(n int) PromOpt             { return func(p *PromDecider) { p.minSamples = n } }
func WithPromClock(fn func() time.Time) PromOpt    { return func(p *PromDecider) { p.now = fn } }

func NewPromDecider(promURL string, opts ...PromOpt) *PromDecider {
	p := &PromDecider{
		client:     &http.Client{Timeout: 5 * time.Second},
		promURL:    promURL,
		threshold:  0.01,
		window:     5 * time.Minute,
		pollEvery:  30 * time.Second,
		minSamples: 100,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *PromDecider) Decide(ctx context.Context, env string) (Decision, Observation, error) {
	deadline := p.now().Add(p.window)
	obs := Observation{Window: p.window, Threshold: p.threshold}
	errQ := fmt.Sprintf(`sum(increase(markup_decide_total{outcome="error",env=%q}[%s]))`, env, p.window)
	totalQ := fmt.Sprintf(`sum(increase(markup_decide_total{env=%q}[%s]))`, env, p.window)
	tick := time.NewTicker(p.pollEvery)
	defer tick.Stop()
	for {
		errCount, total, err := p.sample(ctx, errQ, totalQ)
		if err != nil {
			return DecisionInconclusive, obs, fmt.Errorf("%w: %v", ErrUpstreamUnreachable, err)
		}
		obs.SampleCount = total
		if total > 0 {
			obs.ErrorRate = float64(errCount) / float64(total)
		}
		if obs.SampleCount >= p.minSamples && obs.ErrorRate > p.threshold {
			return DecisionRolledBack, obs, nil
		}
		if !p.now().Before(deadline) {
			if obs.SampleCount < p.minSamples {
				return DecisionInconclusive, obs, nil
			}
			return DecisionKept, obs, nil
		}
		select {
		case <-ctx.Done():
			return DecisionInconclusive, obs, ctx.Err()
		case <-tick.C:
		}
	}
}

func (p *PromDecider) sample(ctx context.Context, errQ, totalQ string) (errCount, total int, err error) {
	errVal, err := p.queryScalar(ctx, errQ)
	if err != nil {
		return 0, 0, err
	}
	totalVal, err := p.queryScalar(ctx, totalQ)
	if err != nil {
		return 0, 0, err
	}
	return int(errVal), int(totalVal), nil
}

func (p *PromDecider) queryScalar(ctx context.Context, query string) (float64, error) {
	u := p.promURL + "/api/v1/query?query=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("prom query %q: %s", query, resp.Status)
	}
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	if len(body.Data.Result) == 0 {
		return 0, nil
	}
	str, _ := body.Data.Result[0].Value[1].(string)
	return strconv.ParseFloat(str, 64)
}
