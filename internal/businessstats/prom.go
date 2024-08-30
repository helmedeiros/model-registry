package businessstats

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

type PromReader struct {
	client  *http.Client
	promURL string
	topN    int
}

type PromOpt func(*PromReader)

func WithPromClient(c *http.Client) PromOpt { return func(p *PromReader) { p.client = c } }
func WithTopN(n int) PromOpt                { return func(p *PromReader) { p.topN = n } }

func NewPromReader(promURL string, opts ...PromOpt) *PromReader {
	p := &PromReader{
		client:  &http.Client{Timeout: 5 * time.Second},
		promURL: strings.TrimRight(promURL, "/"),
		topN:    5,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// promDuration formats a Go duration as a single-unit PromQL range
// specifier (e.g. "300s"). time.Duration.String() emits compound
// values like "5m0s" which PromQL rejects.
func promDuration(d time.Duration) string {
	return strconv.Itoa(int(d.Seconds())) + "s"
}

func (p *PromReader) Stats(ctx context.Context, env string, since time.Duration) (Stats, error) {
	out := Stats{Env: env, Since: since}
	w := promDuration(since)

	var (
		ok, errRate, nm, total       float64
		f50, f95, f99                float64
		rules                        []RuleHit
	)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { ok, err = p.scalar(gctx, fmt.Sprintf(`sum(rate(markup_decide_total{outcome="ok",env=%q}[%s]))`, env, w)); return })
	g.Go(func() (err error) { errRate, err = p.scalar(gctx, fmt.Sprintf(`sum(rate(markup_decide_total{outcome="error",env=%q}[%s]))`, env, w)); return })
	g.Go(func() (err error) { nm, err = p.scalar(gctx, fmt.Sprintf(`sum(rate(markup_decide_total{outcome="no_match",env=%q}[%s]))`, env, w)); return })
	g.Go(func() (err error) { total, err = p.scalar(gctx, fmt.Sprintf(`sum(rate(markup_decide_total{env=%q}[%s]))`, env, w)); return })
	g.Go(func() (err error) { f50, err = p.scalar(gctx, fmt.Sprintf(`histogram_quantile(0.50, sum by (le) (rate(markup_factor_seconds_bucket{env=%q}[%s])))`, env, w)); return })
	g.Go(func() (err error) { f95, err = p.scalar(gctx, fmt.Sprintf(`histogram_quantile(0.95, sum by (le) (rate(markup_factor_seconds_bucket{env=%q}[%s])))`, env, w)); return })
	g.Go(func() (err error) { f99, err = p.scalar(gctx, fmt.Sprintf(`histogram_quantile(0.99, sum by (le) (rate(markup_factor_seconds_bucket{env=%q}[%s])))`, env, w)); return })
	g.Go(func() (err error) { rules, err = p.topRules(gctx, env, w); return })

	if err := g.Wait(); err != nil {
		return out, err
	}

	out.DecideRPS = OutcomeRPS{OK: ok, Error: errRate, NoMatch: nm, Total: total}
	out.FactorP50, out.FactorP95, out.FactorP99 = f50, f95, f99
	out.TopRules = rules
	return out, nil
}

func (p *PromReader) topRules(ctx context.Context, env, window string) ([]RuleHit, error) {
	q := fmt.Sprintf(`topk(%d, sum by (rule) (rate(markup_decide_total{env=%q}[%s])))`, p.topN, env, window)
	return p.vector(ctx, q)
}

func (p *PromReader) scalar(ctx context.Context, query string) (float64, error) {
	body, err := p.runQuery(ctx, query)
	if err != nil {
		return 0, err
	}
	if len(body.Data.Result) == 0 {
		return 0, nil
	}
	return parseValue(body.Data.Result[0].Value)
}

func (p *PromReader) vector(ctx context.Context, query string) ([]RuleHit, error) {
	body, err := p.runQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make([]RuleHit, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		v, perr := parseValue(r.Value)
		if perr != nil {
			// NaN/Inf from sparse histograms — omit rather than surface a meaningless rate.
			continue
		}
		out = append(out, RuleHit{Rule: r.Metric["rule"], RatePerSecond: v})
	}
	return out, nil
}

type promBody struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func (p *PromReader) runQuery(ctx context.Context, query string) (*promBody, error) {
	u, err := url.Parse(p.promURL + "/api/v1/query")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("prom query: status %s", resp.Status)
	}
	var body promBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prom query: status %q", body.Status)
	}
	return &body, nil
}

func parseValue(v [2]any) (float64, error) {
	s, _ := v[1].(string)
	return strconv.ParseFloat(s, 64)
}
