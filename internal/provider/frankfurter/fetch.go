package frankfurter

import (
	"context"
	"errors"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxResponseBytes int64 = 64 << 10

func (c *Client) Fetch(ctx context.Context, pair domain.Pair) (*domain.Quote, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := pair.Validate(); err != nil {
		return nil, err
	}
	endpoint := c.baseURL.JoinPath("rate", strings.ToLower(string(pair.Base)), strings.ToLower(string(pair.Quote)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, invalidResponse(err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "banco-plata-quotes/1.0")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, unavailable(err)
	}
	defer resp.Body.Close()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, unavailable(err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if int64(len(data)) > maxResponseBytes {
		return nil, invalidResponse(errors.New("response exceeds size limit"))
	}
	dto, err := decodeRate(data)
	if err != nil {
		return nil, invalidResponse(err)
	}
	quote, err := toQuote(*dto, pair)
	if err != nil {
		return nil, invalidResponse(err)
	}
	return quote, nil
}
