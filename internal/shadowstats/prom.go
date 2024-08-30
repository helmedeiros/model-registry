package shadowstats

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
}

type PromOpt func(*PromReader)

func WithPromClient(c *http.Client) PromOpt { return func(p *PromReader) { p.client = c } }

func NewPromReader(promURL string, opts ...PromOpt) *PromReader {
	p := &PromReader{
		client:  &http.Client{Timeout: 5 * time.Second},
		promURL: strings.TrimRight(promURL, "/"),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func promWindow(d time.Duration) string {
	return strconv.Itoa(int(d.Seconds())) + "s"
}

func (p *PromReader) Stats(ctx context.Context, since time.Duration) (Stats, error) {
	out := Stats{Since: since}
	w := promWindow(since)

	var (
		agreeTrue, agreeFalse                   float64
		samples                                  float64
		sampledTrue, sampledFalse                float64
		champOnly, challOnly, timeouts, errs    float64
		p50, p95, p99                            float64
		latP50, latP95, latP99                   float64
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(14)
	g.Go(func() (err error) {
		agreeTrue, err = p.scalar(gctx, fmt.Sprintf(`sum(rate(markup_challenger_agreement_total{agree="true"}[%s]))`, w))
		return
	})
	g.Go(func() (err error) {
		agreeFalse, err = p.scalar(gctx, fmt.Sprintf(`sum(rate(markup_challenger_agreement_total{agree="false"}[%s]))`, w))
		return
	})
	g.Go(func() (err error) {
		samples, err = p.scalar(gctx, fmt.Sprintf(`sum(increase(markup_challenger_agreement_total[%s]))`, w))
		return
	})
	g.Go(func() (err error) {
		champOnly, err = p.scalar(gctx, fmt.Sprintf(`sum(rate(markup_challenger_one_sided_total{side="champion_only"}[%s]))`, w))
		return
	})
	g.Go(func() (err error) {
		challOnly, err = p.scalar(gctx, fmt.Sprintf(`sum(rate(markup_challenger_one_sided_total{side="challenger_only"}[%s]))`, w))
		return
	})
	g.Go(func() (err error) {
		timeouts, err = p.scalar(gctx, fmt.Sprintf(`sum(rate(markup_challenger_eval_timeout_total[%s]))`, w))
		return
	})
	g.Go(func() (err error) {
		errs, err = p.scalar(gctx, fmt.Sprintf(`sum(rate(markup_challenger_eval_errors_total[%s]))`, w))
		return
	})
	g.Go(func() (err error) {
		p50, err = p.scalar(gctx, fmt.Sprintf(`histogram_quantile(0.50, sum by (le) (rate(markup_challenger_factor_delta_bucket[%s])))`, w))
		return
	})
	g.Go(func() (err error) {
		p95, err = p.scalar(gctx, fmt.Sprintf(`histogram_quantile(0.95, sum by (le) (rate(markup_challenger_factor_delta_bucket[%s])))`, w))
		return
	})
	g.Go(func() (err error) {
		p99, err = p.scalar(gctx, fmt.Sprintf(`histogram_quantile(0.99, sum by (le) (rate(markup_challenger_factor_delta_bucket[%s])))`, w))
		return
	})
	g.Go(func() (err error) {
		sampledTrue, err = p.scalar(gctx, fmt.Sprintf(`sum(rate(markup_challenger_sampled_total{sampled="true"}[%s]))`, w))
		return
	})
	g.Go(func() (err error) {
		sampledFalse, err = p.scalar(gctx, fmt.Sprintf(`sum(rate(markup_challenger_sampled_total{sampled="false"}[%s]))`, w))
		return
	})
	g.Go(func() (err error) {
		latP50, err = p.scalar(gctx, fmt.Sprintf(`histogram_quantile(0.50, sum by (le) (rate(markup_challenger_decide_duration_seconds_bucket[%s])))`, w))
		return
	})
	g.Go(func() (err error) {
		latP95, err = p.scalar(gctx, fmt.Sprintf(`histogram_quantile(0.95, sum by (le) (rate(markup_challenger_decide_duration_seconds_bucket[%s])))`, w))
		return
	})
	g.Go(func() (err error) {
		latP99, err = p.scalar(gctx, fmt.Sprintf(`histogram_quantile(0.99, sum by (le) (rate(markup_challenger_decide_duration_seconds_bucket[%s])))`, w))
		return
	})
	if err := g.Wait(); err != nil {
		return out, err
	}
	total := agreeTrue + agreeFalse
	if total > 0 {
		out.AgreementRate = agreeTrue / total
	}
	out.AgreementSamples = samples
	out.OneSidedChampionRPS = champOnly
	out.OneSidedChallengerRPS = challOnly
	out.TimeoutRPS = timeouts
	out.ErrorRPS = errs
	out.FactorDeltaP50 = p50
	out.FactorDeltaP95 = p95
	out.FactorDeltaP99 = p99
	if sampledTotal := sampledTrue + sampledFalse; sampledTotal > 0 {
		out.EffectiveSampleRate = sampledTrue / sampledTotal
	}
	out.ChallengerLatencyP50 = latP50
	out.ChallengerLatencyP95 = latP95
	out.ChallengerLatencyP99 = latP99
	return out, nil
}

type promBody struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Value [2]any `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func (p *PromReader) scalar(ctx context.Context, query string) (float64, error) {
	u, err := url.Parse(p.promURL + "/api/v1/query")
	if err != nil {
		return 0, err
	}
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("prom: status %s", resp.Status)
	}
	var body promBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	if body.Status != "success" {
		return 0, fmt.Errorf("prom: status %q", body.Status)
	}
	if len(body.Data.Result) == 0 {
		return 0, nil
	}
	s, _ := body.Data.Result[0].Value[1].(string)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("prom: parse value %q: %w", s, err)
	}
	return v, nil
}
