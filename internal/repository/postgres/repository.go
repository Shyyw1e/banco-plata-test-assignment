package postgres

import (
	"database/sql"
	"time"

	"github.com/Shyyw1e/banco-plata-test-assignment/internal/repository"
)

type Repository struct {
	db               *sql.DB
	operationTimeout time.Duration
	maxPending       int
}

func New(db *sql.DB, operationTimeout time.Duration, maxPending int) *Repository {
	return &Repository{
		db:               db,
		operationTimeout: operationTimeout,
		maxPending:       maxPending,
	}
}

var _ repository.UpdateReader = (*Repository)(nil)

var _ repository.UpdateCreator = (*Repository)(nil)
