package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// maxStreamBytes caps doStream output so a misbehaving registry
// cannot fill the operator's disk or pipe-consumer.
const maxStreamBytes int64 = 512 << 20

// configurePropagator wires the global W3C TextMapPropagator. Called
// from Run rather than init() so importing the package does not mutate
// process state.
func configurePropagator() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// doJSON GETs base+path and decodes the JSON body into v.
func doJSON(ctx context.Context, c *http.Client, base, path string, query url.Values, v any) (int, error) {
	resp, err := doGET(ctx, c, base, path, query)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, fmt.Errorf("registry: %s: %s", resp.Status, string(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return resp.StatusCode, fmt.Errorf("decode body: %w", err)
	}
	return resp.StatusCode, nil
}

// doStream issues a GET and copies the body straight to w. Used for
// `mrctl artifact <hash> source` and similar byte-stream endpoints.
func doStream(ctx context.Context, c *http.Client, base, path string, w io.Writer) (int, error) {
	resp, err := doGET(ctx, c, base, path, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, fmt.Errorf("registry: %s: %s", resp.Status, string(body))
	}
	limited := io.LimitReader(resp.Body, maxStreamBytes+1)
	n, err := io.Copy(w, limited)
	if err != nil {
		return resp.StatusCode, fmt.Errorf("stream body: %w", err)
	}
	if n > maxStreamBytes {
		return resp.StatusCode, fmt.Errorf("registry: stream exceeded %d bytes; output truncated", maxStreamBytes)
	}
	return resp.StatusCode, nil
}

func doGET(ctx context.Context, c *http.Client, base, path string, query url.Values) (*http.Response, error) {
	u, err := buildURL(base, path, query)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	return resp, nil
}

// postJSON POSTs body to base+path with the supplied Content-Type and
// decodes the JSON response into v. Used by the write subcommands.
func postJSON(ctx context.Context, c *http.Client, base, path string, body io.Reader, contentType string, v any) (int, error) {
	u, err := buildURL(base, path, nil)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := c.Do(req)
	if err != nil {
		return 0, fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, fmt.Errorf("registry: %s: %s", resp.Status, string(raw))
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return resp.StatusCode, fmt.Errorf("decode body: %w", err)
	}
	return resp.StatusCode, nil
}

func buildURL(base, path string, query url.Values) (*url.URL, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	u.Path = path
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u, nil
}
