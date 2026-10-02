package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase"
)

type fakeRepository struct {
	create func(context.Context, domain.UpdateID, domain.Pair, *string) (*domain.QuoteUpdate, error)
	byID   func(context.Context, domain.UpdateID) (*domain.QuoteUpdate, error)
	latest func(context.Context, domain.Pair) (*domain.QuoteUpdate, error)
}

func (f fakeRepository) CreateOrGet(ctx context.Context, id domain.UpdateID, pair domain.Pair, key *string) (*domain.QuoteUpdate, error) {
	return f.create(ctx, id, pair, key)
}
func (f fakeRepository) GetByID(ctx context.Context, id domain.UpdateID) (*domain.QuoteUpdate, error) {
	return f.byID(ctx, id)
}
func (f fakeRepository) GetLatest(ctx context.Context, pair domain.Pair) (*domain.QuoteUpdate, error) {
	return f.latest(ctx, pair)
}

var pair = domain.Pair{Base: domain.EUR, Quote: domain.USD}
var id = domain.UpdateID{15: 1}

type operation struct {
	name string
	call func(*usecase.Service, context.Context) (*domain.QuoteUpdate, error)
}

var operations = []operation{
	{"create", func(s *usecase.Service, ctx context.Context) (*domain.QuoteUpdate, error) {
		return s.CreateUpdate(ctx, pair, nil)
	}},
	{"by ID", func(s *usecase.Service, ctx context.Context) (*domain.QuoteUpdate, error) { return s.GetByID(ctx, id) }},
	{"latest", func(s *usecase.Service, ctx context.Context) (*domain.QuoteUpdate, error) {
		return s.GetLatest(ctx, pair)
	}},
}

func TestCanceledContextSkipsRepository(t *testing.T) {
	for _, op := range operations {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline=%t", op.name, deadline), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				want := context.Canceled
				if deadline {
					cancel()
					ctx, cancel = context.WithDeadline(context.Background(), time.Unix(0, 0))
					want = context.DeadlineExceeded
				} else {
					cancel()
				}
				defer cancel()
				// Nil dependencies ensure no repository call can go unnoticed.
				got, err := op.call(usecase.NewService(nil, nil), ctx)
				if got != nil || !errors.Is(err, want) {
					t.Fatalf("got %v, %v; want nil, %v", got, err, want)
				}
			})
		}
	}
}

func TestInvalidPairsSkipRepository(t *testing.T) {
	cases := []struct {
		name string
		pair domain.Pair
		err  error
	}{
		{"empty", domain.Pair{}, domain.ErrInvalidPair},
		{"lowercase", domain.Pair{Base: "eur", Quote: domain.USD}, domain.ErrInvalidPair},
		{"same", domain.Pair{Base: domain.USD, Quote: domain.USD}, domain.ErrSameCurrency},
		{"unsupported", domain.Pair{Base: "GBP", Quote: domain.USD}, domain.ErrUnsupportedPair},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := usecase.NewService(nil, nil)
			got, err := s.CreateUpdate(context.Background(), tc.pair, nil)
			if got != nil || !errors.Is(err, tc.err) {
				t.Fatalf("create: %v, %v", got, err)
			}
			got, err = s.GetLatest(context.Background(), tc.pair)
			if got != nil || !errors.Is(err, tc.err) {
				t.Fatalf("latest: %v, %v", got, err)
			}
		})
	}
}

func TestInvalidIdempotencyKeysSkipRepository(t *testing.T) {
	for _, key := range []string{"", strings.Repeat("a", 129), "a b", "\t", "\r", "\n", "\x00", "\x1f", "\x7f", "\x80", "КЛЮЧ", "é"} {
		t.Run(fmt.Sprintf("%q", key), func(t *testing.T) {
			got, err := usecase.NewService(nil, nil).CreateUpdate(context.Background(), pair, &key)
			if got != nil || !errors.Is(err, usecase.ErrInvalidIdempotencyKey) {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestCreatePassesOpaqueKeyAndGeneratedID(t *testing.T) {
	ascii := ""
	for c := byte('!'); c <= '~'; c++ {
		ascii += string(c)
	}
	keys := []*string{nil}
	for _, key := range []string{"a", "Key-aB_123", strings.Repeat("z", 128), ascii} {
		keys = append(keys, &key)
	}
	for i, key := range keys {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := &domain.QuoteUpdate{ID: id, Pair: pair, Status: domain.StatusQueued}
			calls := 0
			f := fakeRepository{create: func(actual context.Context, generated domain.UpdateID, p domain.Pair, k *string) (*domain.QuoteUpdate, error) {
				calls++
				if actual != ctx || p != pair || k != key {
					t.Fatal("arguments changed")
				}
				if generated == (domain.UpdateID{}) || generated[6]>>4 != 4 || generated[8]>>6 != 2 {
					t.Fatalf("not a UUID v4: %v", generated)
				}
				// Existing idempotent result can have an ID different from the proposed ID.
				return want, nil
			}}
			got, err := usecase.NewService(f, f).CreateUpdate(ctx, pair, key)
			if err != nil || got != want || calls != 1 {
				t.Fatalf("got %v, %v; calls=%d", got, err, calls)
			}
		})
	}
}

func TestReadsPassArgumentsAndReturnRepositoryResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, queryID := range []domain.UpdateID{id, {}} {
		t.Run(queryID.String(), func(t *testing.T) {
			want := &domain.QuoteUpdate{ID: queryID, Pair: pair, Status: domain.StatusQueued}
			calls := 0
			f := fakeRepository{byID: func(actual context.Context, actualID domain.UpdateID) (*domain.QuoteUpdate, error) {
				calls++
				if actual != ctx || actualID != queryID {
					t.Fatal("arguments changed")
				}
				return want, nil
			}}
			got, err := usecase.NewService(f, f).GetByID(ctx, queryID)
			if err != nil || got != want || calls != 1 {
				t.Fatalf("got %v, %v; calls=%d", got, err, calls)
			}
		})
	}
	want := &domain.QuoteUpdate{ID: id, Pair: pair, Status: domain.StatusSucceeded}
	calls := 0
	f := fakeRepository{latest: func(actual context.Context, p domain.Pair) (*domain.QuoteUpdate, error) {
		calls++
		if actual != ctx || p != pair {
			t.Fatal("arguments changed")
		}
		return want, nil
	}}
	got, err := usecase.NewService(f, f).GetLatest(ctx, pair)
	if err != nil || got != want || calls != 1 {
		t.Fatalf("got %v, %v; calls=%d", got, err, calls)
	}
}

func TestRepositoryErrorsPreserveCause(t *testing.T) {
	for _, op := range operations {
		for _, cause := range []error{repository.ErrNotFound, repository.ErrIdempotencyConflict, repository.ErrQueueFull, repository.ErrUnavailable, context.Canceled, context.DeadlineExceeded, errors.New("dependency failure")} {
			t.Run(op.name+"/"+cause.Error(), func(t *testing.T) {
				calls := 0
				fail := func() (*domain.QuoteUpdate, error) { calls++; return nil, fmt.Errorf("storage: %w", cause) }
				f := fakeRepository{
					create: func(context.Context, domain.UpdateID, domain.Pair, *string) (*domain.QuoteUpdate, error) {
						return fail()
					},
					byID:   func(context.Context, domain.UpdateID) (*domain.QuoteUpdate, error) { return fail() },
					latest: func(context.Context, domain.Pair) (*domain.QuoteUpdate, error) { return fail() },
				}
				got, err := op.call(usecase.NewService(f, f), context.Background())
				if got != nil || !errors.Is(err, cause) || calls != 1 {
					t.Fatalf("got %v, %v; calls=%d", got, err, calls)
				}
			})
		}
	}
}

func TestCancellationDuringRepositoryCall(t *testing.T) {
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop := func(actual context.Context) (*domain.QuoteUpdate, error) {
				if actual != ctx {
					t.Fatal("context replaced")
				}
				cancel()
				return nil, actual.Err()
			}
			f := fakeRepository{
				create: func(c context.Context, _ domain.UpdateID, _ domain.Pair, _ *string) (*domain.QuoteUpdate, error) {
					return stop(c)
				},
				byID:   func(c context.Context, _ domain.UpdateID) (*domain.QuoteUpdate, error) { return stop(c) },
				latest: func(c context.Context, _ domain.Pair) (*domain.QuoteUpdate, error) { return stop(c) },
			}
			got, err := op.call(usecase.NewService(f, f), ctx)
			if got != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestSuccessfulCreateIsNotOverriddenByLateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := &domain.QuoteUpdate{ID: id, Pair: pair, Status: domain.StatusQueued}
	f := fakeRepository{create: func(context.Context, domain.UpdateID, domain.Pair, *string) (*domain.QuoteUpdate, error) {
		cancel()
		return want, nil
	}}
	got, err := usecase.NewService(f, f).CreateUpdate(ctx, pair, nil)
	if got != want || err != nil {
		t.Fatalf("committed result lost: %v, %v", got, err)
	}
}
