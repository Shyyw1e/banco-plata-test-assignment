package domain_test

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
)

func TestPricePrecisionAndBounds(t *testing.T) {
	cases := []struct{ input, want string }{
		{"20.1234", "20.1234000000"},
		{"1.00000000005", "1.0000000000"},
		{"1.00000000015", "1.0000000002"},
		{"0.00000000006", "0.0000000001"},
		{"9999999999.9999999999", "9999999999.9999999999"},
		{"2.01234e1", "20.1234000000"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := domain.ParsePrice(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want {
				t.Fatalf("got %s, want %s", got.String(), tc.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatal(err)
			}
			if got.Decimal().StringFixed(10) != tc.want {
				t.Fatal("decimal accessor changed value")
			}
		})
	}
	for _, raw := range []string{"", "0", "-1", "NaN", "Infinity", "bad", "0.00000000005", "9999999999.99999999995", "1e10", "1e2147483647", "1e-2147483648", strings.Repeat("1", 129)} {
		t.Run("invalid/"+raw, func(t *testing.T) {
			if _, err := domain.ParsePrice(raw); !errors.Is(err, domain.ErrInvalidPrice) {
				t.Fatalf("got %v", err)
			}
		})
	}
	price, err := domain.NewPrice(201234, -4)
	if err != nil || price.String() != "20.1234000000" {
		t.Fatalf("got %v, %v", price, err)
	}
	for _, exp := range []int32{math.MinInt32, math.MaxInt32} {
		if _, err := domain.NewPrice(1, exp); !errors.Is(err, domain.ErrInvalidPrice) {
			t.Fatalf("exponent %d: %v", exp, err)
		}
	}
	if err := (domain.Price{}).Validate(); !errors.Is(err, domain.ErrInvalidPrice) {
		t.Fatal("zero price accepted")
	}
}

func TestPairs(t *testing.T) {
	for _, raw := range []string{"eur/usd", "eur/mxn", "usd/eur", "usd/mxn", "mxn/eur", "mxn/usd"} {
		pair, err := domain.ParsePair(raw)
		if err != nil {
			t.Fatal(err)
		}
		if pair.String() != strings.ToUpper(raw) {
			t.Fatalf("not normalized: %s", pair)
		}
	}
	for _, tc := range []struct {
		raw  string
		want error
	}{
		{"EUR/EUR", domain.ErrSameCurrency}, {"eur/EUR", domain.ErrSameCurrency},
		{"GBP/USD", domain.ErrUnsupportedPair}, {"EURUSD", domain.ErrInvalidPair},
		{" EUR/USD", domain.ErrInvalidPair}, {"12A/USD", domain.ErrInvalidPair},
		{"", domain.ErrInvalidPair}, {"EUR/", domain.ErrInvalidPair},
	} {
		pair, err := domain.ParsePair(tc.raw)
		if pair != nil || !errors.Is(err, tc.want) {
			t.Fatalf("%q: got %v, %v", tc.raw, pair, err)
		}
	}
	if _, err := domain.NewPair("uſd", domain.EUR); !errors.Is(err, domain.ErrInvalidPair) {
		t.Fatalf("non-ASCII currency accepted: %v", err)
	}
}

func TestUpdateIDs(t *testing.T) {
	id, err := domain.NewUpdateID()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := domain.ParseUpdateID(strings.ToUpper(id.String()))
	if err != nil || parsed != id {
		t.Fatalf("round trip: %v", err)
	}
	for _, raw := range []string{"", "hello", "{" + id.String() + "}", strings.ReplaceAll(id.String(), "-", ""), "urn:uuid:" + id.String(), "zzzzzzzz-0000-0000-0000-000000000000"} {
		if _, err := domain.ParseUpdateID(raw); !errors.Is(err, domain.ErrInvalidID) {
			t.Fatalf("%q: %v", raw, err)
		}
	}
	// Syntactic validity does not establish existence in the database.
	if _, err := domain.ParseUpdateID("00000000-0000-0000-0000-000000000000"); err != nil {
		t.Fatal(err)
	}
}

func validResult(t *testing.T) (domain.UpdateID, domain.Pair, *domain.UpdateResult, time.Time) {
	t.Helper()
	pair, err := domain.NewPair(domain.EUR, domain.MXN)
	if err != nil {
		t.Fatal(err)
	}
	price, err := domain.ParsePrice("20.1234")
	if err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2026, 10, 1, 0, 30, 0, 0, time.FixedZone("UTC+3", 3*60*60))
	quote, err := domain.NewQuote(*pair, price, "frankfurter:ecb", instant)
	if err != nil {
		t.Fatal(err)
	}
	if !quote.SourceDate.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("calendar date was shifted")
	}
	result, err := domain.NewUpdateResult(*quote, instant)
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdatedAt.Equal(instant) || result.UpdatedAt.Location() != time.UTC {
		t.Fatal("timestamp not normalized to UTC")
	}
	id, err := domain.NewUpdateID()
	if err != nil {
		t.Fatal(err)
	}
	return id, *pair, result, instant
}

func TestQuoteAndResultValidation(t *testing.T) {
	_, pair, result, instant := validResult(t)
	for _, source := range []string{"", " ", " frankfurter:ecb"} {
		if q, err := domain.NewQuote(pair, result.Quote.Price, source, instant); q != nil || !errors.Is(err, domain.ErrInvalidQuote) {
			t.Fatalf("source %q accepted", source)
		}
	}
	if _, err := domain.NewQuote(domain.Pair{}, result.Quote.Price, "ecb", instant); !errors.Is(err, domain.ErrInvalidPair) {
		t.Fatal(err)
	}
	if _, err := domain.NewQuote(pair, domain.Price{}, "ecb", instant); !errors.Is(err, domain.ErrInvalidPrice) {
		t.Fatal(err)
	}
	if _, err := domain.NewQuote(pair, result.Quote.Price, "ecb", time.Time{}); !errors.Is(err, domain.ErrInvalidQuote) {
		t.Fatal(err)
	}
	if _, err := domain.NewUpdateResult(result.Quote, time.Time{}); !errors.Is(err, domain.ErrInvalidResult) {
		t.Fatal(err)
	}
	if _, err := domain.NewUpdateResult(domain.Quote{}, instant); !errors.Is(err, domain.ErrInvalidResult) {
		t.Fatal(err)
	}
	bad := result.Quote
	bad.SourceDate = bad.SourceDate.Add(time.Hour)
	if err := bad.Validate(); !errors.Is(err, domain.ErrInvalidQuote) {
		t.Fatal("non-midnight date accepted")
	}
}

func TestUpdateStateInvariants(t *testing.T) {
	id, pair, result, instant := validResult(t)
	update, err := domain.NewQuoteUpdate(id, pair, instant)
	if err != nil {
		t.Fatal(err)
	}
	if update.Status != domain.StatusQueued || update.Result != nil || update.ErrorCode != nil || !update.CreatedAt.Equal(instant) || update.CreatedAt.Location() != time.UTC {
		t.Fatal("invalid initial state")
	}
	code := domain.CodeUnavailable
	unknown := domain.FailureCode("unknown")
	cases := []struct {
		name   string
		change func(*domain.QuoteUpdate)
		valid  bool
	}{
		{"queued", func(u *domain.QuoteUpdate) {}, true},
		{"processing", func(u *domain.QuoteUpdate) { u.Status = domain.StatusProcessing }, true},
		{"succeeded", func(u *domain.QuoteUpdate) { u.Status = domain.StatusSucceeded; u.Result = result }, true},
		{"failed", func(u *domain.QuoteUpdate) { u.Status = domain.StatusFailed; u.ErrorCode = &code }, true},
		{"queued result", func(u *domain.QuoteUpdate) { u.Result = result }, false},
		{"queued error", func(u *domain.QuoteUpdate) { u.ErrorCode = &code }, false},
		{"success missing result", func(u *domain.QuoteUpdate) { u.Status = domain.StatusSucceeded }, false},
		{"success with error", func(u *domain.QuoteUpdate) { u.Status = domain.StatusSucceeded; u.Result = result; u.ErrorCode = &code }, false},
		{"failed missing code", func(u *domain.QuoteUpdate) { u.Status = domain.StatusFailed }, false},
		{"failed unknown code", func(u *domain.QuoteUpdate) { u.Status = domain.StatusFailed; u.ErrorCode = &unknown }, false},
		{"unknown state", func(u *domain.QuoteUpdate) { u.Status = "unknown" }, false},
		{"zero ID", func(u *domain.QuoteUpdate) { u.ID = domain.UpdateID{} }, false},
		{"zero time", func(u *domain.QuoteUpdate) { u.CreatedAt = time.Time{} }, false},
		{"pair mismatch", func(u *domain.QuoteUpdate) {
			u.Status = domain.StatusSucceeded
			u.Result = result
			u.Pair = domain.Pair{Base: domain.USD, Quote: domain.MXN}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := *update
			tc.change(&copy)
			err := copy.Validate()
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && !errors.Is(err, domain.ErrInvalidUpdate) {
				t.Fatalf("expected invalid update, got %v", err)
			}
		})
	}
	if got, err := domain.NewQuoteUpdate(id, pair, time.Time{}); got != nil || !errors.Is(err, domain.ErrInvalidUpdate) {
		t.Fatal("constructor accepted invalid update")
	}
}

func TestAttemptLease(t *testing.T) {
	id, pair, _, instant := validResult(t)
	// Expiry is checked by the repository using its clock, not time.Now in domain.
	attempt, err := domain.NewAttempt(id, pair, 2, instant)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Number != 2 || !attempt.LeaseUntil.Equal(instant) || attempt.LeaseUntil.Location() != time.UTC {
		t.Fatal("attempt fields changed")
	}
	for _, n := range []int64{0, -1} {
		if got, err := domain.NewAttempt(id, pair, n, instant); got != nil || !errors.Is(err, domain.ErrInvalidAttempt) {
			t.Fatalf("attempt %d accepted", n)
		}
	}
	if _, err := domain.NewAttempt(id, pair, 1, time.Time{}); !errors.Is(err, domain.ErrInvalidAttempt) {
		t.Fatal(err)
	}
	if _, err := domain.NewAttempt(domain.UpdateID{}, pair, 1, instant); !errors.Is(err, domain.ErrInvalidAttempt) {
		t.Fatal(err)
	}
	if _, err := domain.NewAttempt(id, domain.Pair{}, 1, instant); !errors.Is(err, domain.ErrInvalidPair) {
		t.Fatal(err)
	}
}
