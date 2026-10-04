.PHONY: db-up db-stop migrate-up migrate-version migrate-down

db-up:
	docker compose up -d --wait postgres

db-stop:
	docker compose stop postgres

migrate-up:
	docker compose run --rm migrate up

migrate-version:
	docker compose run --rm migrate version

# Explicitly requested rollback of ONE migration; drops its table and data.
migrate-down:
	docker compose run --rm migrate down 1
