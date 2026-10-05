# Сервис валютных котировок

Асинхронный HTTP/JSON-сервис на Go 1.25.2. POST сохраняет задание и возвращает ID; воркеры получают курс Frankfurter/ECB и сохраняют результат в PostgreSQL. GET читает сохранённые данные, не обращаясь к провайдеру.

Поддерживаются пары `EUR/USD`, `USD/EUR`, `EUR/MXN`, `MXN/EUR`, `USD/MXN`, `MXN/USD`. Это дневные справочные курсы, а не поток рыночных цен. Повторное обновление может вернуть прежнюю цену и дату источника.

## Настройки и запуск

Нужны Go **1.25.2**, Docker с Compose v2 и `curl`. В примерах обработки JSON используется Python 3. Проверьте `go version`. Команды выполняются из корня репозитория.

```sh
cp .env.example .env
```

Проверьте настройки `.env`: для локального запуска `DATABASE_URL` должен соответствовать `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD` и `POSTGRES_PORT`. Значения шаблона предназначены для локальной разработки. `.env` исключён из Git и Docker-образа.

Поднимите БД, примените миграции и запустите сервис:

```sh
docker compose --env-file .env up -d --wait postgres
docker compose --env-file .env run --rm migrate up
make build
./bin/quotes -env-file .env
```

Без `-env-file` приложение читает переменные процесса и значения по умолчанию; `DATABASE_URL` обязательна. Приоритет: окружение процесса → явно указанный dotenv-файл → defaults. Явно пустое значение не заменяется default. Загрузка конфигурации не меняет окружение процесса.

Схема и seed провайдера проверяются при старте; приложение не применяет миграции. Повторный `migrate up` не повторяет уже применённые версии. Изменение пароля в `.env` не меняет пароль пользователя в существующем volume.

## POST → получение результата → latest

Примеры ниже используют порт `8080` по умолчанию (`HTTP_ADDR=:8080`). Если в `.env` задан другой `HTTP_ADDR`, подставьте его порт во все URL команд.

В другом терминале проверьте готовность:

```sh
curl --fail --retry 10 --retry-connrefused --retry-delay 1 http://localhost:8080/health/ready
```

Следующий пример создаёт задание, извлекает ID и опрашивает результат до терминального состояния (не дольше примерно 30 секунд):

```sh
(
  set -eu
  receipt=$(curl -fsS -X POST http://localhost:8080/v1/quote-updates \
    -H 'Content-Type: application/json' -d '{"pair":"EUR/USD"}')
  id=$(printf '%s' "$receipt" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
  printf 'Update ID: %s\n' "$id"
  status=''
  for attempt in $(seq 1 60); do
    result=$(curl -fsS "http://localhost:8080/v1/quote-updates/$id")
    status=$(printf '%s' "$result" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')
    case "$status" in succeeded|failed) break ;; esac
    sleep 0.5
  done
  printf '%s\n' "$result"
  case "$status" in succeeded|failed) ;; *) echo 'Polling timeout' >&2; exit 1 ;; esac
  curl -sS -i 'http://localhost:8080/v1/quotes/latest?pair=EUR%2FUSD'
)
```

Первичный POST отвечает `202` и содержит `id`, `status` и заголовок `Location`. `queued`/`processing` означают ожидание; GET по существующему ID возвращает `200` для всех состояний, включая `failed`. У `succeeded` есть цена и время, у `failed` — `error.code`. Latest возвращает `404`, если успешных результатов пары ещё нет.

## Контракт API

Полная спецификация: [api/openapi.yaml](api/openapi.yaml).

| Операция | Назначение |
| --- | --- |
| `POST /v1/quote-updates` | Сохранить задание и вернуть ID после commit |
| `GET /v1/quote-updates/{id}` | Состояние и результат конкретного задания |
| `GET /v1/quotes/latest?pair=EUR%2FUSD` | Последний успешный результат пары |

Цена передаётся десятичной строкой с 10 знаками после точки; float64 для неё не используется. `source_date` — дата курса у источника, `updated_at` — время сохранения результата в UTC, `source` — `frankfurter:ecb`. Latest выбирается по `source_date DESC, completed_at DESC, id DESC`. Новый failed не скрывает прежний успешный результат.

Необязательный `Idempotency-Key` содержит 1–128 печатных ASCII-символов без пробелов. Ключ чувствителен к регистру. Повтор с той же нормализованной парой возвращает прежний ID: `202` для активного задания, `200` для терминального. Другая пара с тем же ключом даёт `409`. Повтор failed не создаёт новое обновление — для него нужен новый ключ. Без ключа каждый POST создаёт отдельное задание. Очистка результатов и ключей пока не реализована; срок хранения не определён.

Неверный формат входных данных даёт `400`, неподдерживаемая пара — `422`. При заполнении очереди или недоступности БД возвращается `503` с `Retry-After`. Обновления инициируются клиентом: автоматического обновления всех пар по расписанию нет.

## Фоновая обработка и остановка

По умолчанию работают 5 воркеров на процесс, polling пустой очереди — 500 мс, lease — 30 с, максимум 3 захвата задания, предел активной очереди — 1000. Recovery выполняется при старте и затем примерно раз в секунду, порциями до 100 строк. Истёкший lease возвращает задание в очередь; исчерпанные попытки завершаются `attempts_exhausted`. Номер попытки и действующий lease защищают результат от запоздавшего воркера.

Общий SQL-limiter выдаёт разрешения с интервалом 500 мс (2 RPS) всем экземплярам вместе, включая повторы. Это настройка приложения, не заявленная квота Frankfurter. После простоя разрешения не накапливаются. `429` продлевает общую паузу; корректный `Retry-After` задаёт её нижнюю границу. Уже начатые запросы не отменяются. Контролируется темп разрешений, а не точное расстояние между сетевыми пакетами.

Сетевые timeout, `408`, `429` и `5xx` допускают retry. База задержки — 1 с, затем 2 с с jitter; backoff с jitter ограничен минутой, `Retry-After` может увеличить его. Повтор хранится в `next_attempt_at` и не занимает воркер ожиданием. Остальные `4xx` и невалидный ответ завершаются без повтора. Последний отказ сохраняет код провайдера.

Circuit breaker общий для воркеров одного процесса: после 5 последовательных временных отказов — пауза 30 с, затем одна проба. Успех закрывает breaker, любой отказ пробы снова открывает его. Ошибки БД и отмена приложения не считаются отказами источника. Параметры задаются в `.env.example`.

`/health/live` проверяет живость процесса. `/health/ready` делает короткий Ping БД и учитывает остановку; отказ провайдера не отключает чтение результатов. `Ctrl+C`/SIGTERM прекращает HTTP-приём и новые итерации воркеров. Активным операциям предоставляется общий `SHUTDOWN_TIMEOUT`, затем их контексты отменяются. Задания в PostgreSQL сохраняются для recovery.

## Запуск в Docker

Вместо запуска локального бинарника, после подготовки БД и миграций:

```sh
docker compose --env-file .env --profile app up -d --build app
curl --fail --retry 10 --retry-connrefused --retry-delay 1 http://localhost:8080/health/ready
docker compose --env-file .env logs -f app
```

Сборка использует Go 1.25.2; runtime `scratch` содержит статический бинарник и CA-сертификаты, работает как UID/GID 65532. Shell и `.env` в образ не входят. Compose передаёт перечисленные настройки переменными процесса. Профиль `app` не запускается командами только для БД.

В контейнере БД доступна по `postgres:5432`. По умолчанию DSN составляется из POSTGRES_USER/PASSWORD/DB; для пароля со специальными символами задайте `APP_DATABASE_URL` с URL-кодированными учётными данными. `APP_PORT` — порт хоста (8080); внутри контейнера сервер слушает 8080. `CONTAINER_STOP_GRACE_PERIOD` (20 с) должен быть больше `SHUTDOWN_TIMEOUT` (15 с).

Остановка с сохранением данных:

```sh
docker compose --env-file .env --profile app down
```

`down -v` удалит данные PostgreSQL. `make db-stop` только останавливает БД. `make migrate-down` откатывает одну миграцию и удаляет её таблицу с данными. Новые изменения схемы оформляются отдельными миграциями.

## Тесты и CI

| Команда | Проверка |
| --- | --- |
| `make build` | Бинарник `bin/quotes` |
| `make test` | Unit-тесты |
| `make race` | Unit-тесты с детектором гонок |
| `make vet` | Статический анализ |
| `make check` | Build, vet, race |
| `make integration` | Миграции и PostgreSQL integration/race |
| `make e2e` | Реальные бинарники, локальный провайдер и PostgreSQL |
| `make docker-build` | Образ `quotes:local` |

Путь к Go можно передать через `make GO=/path/to/go ...`. Integration/E2E требуют Docker: скрипт создаёт уникальный Compose-проект на свободном порту и удаляет его контейнеры, сеть и volume после успеха или ошибки. Используется `.env.example`, личный `.env` не читается.

При ручном запуске `go test -tags=integration` обязательна отдельная `TEST_DATABASE_URL` с миграциями: эти тесты делают TRUNCATE. Тесты `go test -tags=e2e -race ./tests/e2e -timeout=120s` создают собственные схемы в тестовой БД и сами собирают бинарник с race. Не направляйте тесты на рабочую БД.

E2E проверяет шесть пар, POST до ответа источника, GET/latest, retry и постоянные ошибки, исчерпание попыток, сохранение latest после failed, остановку во время Fetch, recovery после рестарта, два экземпляра с общим permit/429 и пробу breaker. Автоматические тесты не обращаются к публичному Frankfurter.

[CI workflow](.github/workflows/ci.yml) выполняет build/vet/unit/race/integration/E2E и Docker build на Go 1.25.2. Команды проверены локально; удалённый запуск GitHub Actions ещё не проверен.

Дополнительная проверка OpenAPI:

```sh
python3 -m venv /tmp/quotes-openapi-check
/tmp/quotes-openapi-check/bin/python -m pip install openapi-spec-validator==0.7.2
/tmp/quotes-openapi-check/bin/python -m openapi_spec_validator api/openapi.yaml
```

## Архитектура, логи и диагностика

`internal/domain` содержит модель и правила данных, `internal/usecase` — сценарии и breaker. Контракты БД находятся в `internal/repository`, SQL — в `repository/postgres`. `transport/httpapi`, `provider/frankfurter` и `worker` реализуют адаптеры. `cmd/quotes` загружает config и собирает зависимости. PostgreSQL используется через `database/sql` с драйвером pgx; HTTP — через `net/http`. Kafka, RabbitMQ, Redis и кэш не используются.

Logger на `log/slog` пишет JSON с полем `service`. Логи попыток содержат `update_id`, `pair`, `attempt`, `outcome`; `processed` означает завершение шага, а не обязательно succeeded. Точное состояние и код отказа читаются через GET. Recovery логирует числа восстановленных/завершённых строк. DSN, ключи идемпотентности и тела ответов провайдера не выводятся; автоматического удаления секретов самим logger нет.

| Наблюдение | Что проверить |
| --- | --- |
| Старт сообщает о схеме или seed | Применены ли обе миграции к той же БД |
| Readiness/POST возвращает 503 | Доступность БД, настройки подключения; для queue_full — число активных заданий |
| Задание долго queued | Общий лимит, backlog, next_attempt_at и пауза после 429/breaker |
| Processing остался после остановки | Дождаться истечения lease и recovery; до expiry задание не перехватывается |
| Failed | Код через GET; новое обновление требует нового ключа либо POST без ключа |

Для локальной диагностики очереди без вывода секретов:

```sh
docker compose --env-file .env exec postgres sh -c \
  'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT status, count(*) FROM quote_updates GROUP BY status;"'
```

## Границы проверенного решения

Проверены unit/race, PostgreSQL integration, E2E реальных процессов и локальный запуск Docker-образа. Финальный прогон по README и подготовка публикации — отдельный P10. Нагрузочный этап остаётся открытым: RPS клиентов, p95/p99 и SLA не измерены, ёмкость очереди 1000 не обещает короткого ожидания.

Метрики, retention/очистка, кэш и уведомления вместо polling относятся к дальнейшему развитию. Инфраструктурный HA исключён из объёма: один PostgreSQL с volume не гарантирует сохранность после потери диска/узла/зоны. RPO/RTO, переключение primary и backup/PITR не реализованы и не заявляются.
