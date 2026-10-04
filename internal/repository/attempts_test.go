package repository_test

import (
	"errors"
	"fmt"
	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
	"testing"
)

func TestBackgroundErrorsAreDistinctFromReadAndAvailability(t *testing.T) {
	cases := []error{repository.ErrNoJob, repository.ErrLeaseLost, repository.ErrUnavailable, repository.ErrNotFound}
	for _, cause := range cases {
		wrapped := fmt.Errorf("attempt operation: %w", cause)
		for _, target := range cases {
			if errors.Is(wrapped, target) != (cause == target) {
				t.Fatalf("%v matched %v incorrectly", cause, target)
			}
		}
	}
}
