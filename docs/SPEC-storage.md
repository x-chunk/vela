# Vela — дополнение к спецификации: бэкенды хранилища (fs + Cloudflare R2)

> Дополнение к `docs/SPEC.md`. Положить в репо как `docs/SPEC-storage.md`. Где этот документ противоречит основному SPEC, **приоритет у этого документа**. Термины MUST / SHOULD / MAY — как в основном SPEC. Перед реализацией задать вопросы из §12.

---

## 1. Зачем

На VPS остался ~1 GB свободного места, этого не хватит даже на перенос текущего `/static/` и школьных материалов. Поэтому содержимое файлов (блобы) должно уметь жить в **Cloudflare R2** (S3-совместимое хранилище, бесплатно до 10 GB, без платы за исходящий трафик; домен `xchunk.org` уже обслуживается Cloudflare).

Локальная реализация **не выкидывается**: оба бэкенда живут рядом, выбор делается конфигурацией.

На VPS в любом режиме остаются только SQLite (метаданные, ключи, ссылки, корзина) и временные файлы. Это мегабайты.

---

## 2. Выбор бэкенда: runtime, а не build tags

Решение (**MUST**): бэкенд выбирается **при запуске** переменной `VELA_STORAGE=fs|r2`, а не при сборке.

Почему не build tags:
- Один образ в ghcr для всех случаев: не нужно собирать и версионировать два варианта, CI проще.
- Локальная разработка и тесты работают на `fs` тем же бинарником, что едет в прод.
- Миграция `fs → r2` (§9) требует, чтобы в одном бинарнике были **оба** бэкенда одновременно.
- Цена — размер бинарника вырастает на несколько МБ из-за `aws-sdk-go-v2`. Для серверного образа это несущественно.

CLI (`vela`) от бэкенда не зависит вообще и ничего про него не знает: S3 SDK MUST NOT попадать в зависимости CLI (проверяется тем, что `cmd/vela` не импортирует `internal/blob`; можно закрепить тестом на `go list -deps`).

---

## 3. Интерфейс `internal/blob`

Сервисный слой работает только с интерфейсом (определяется в `internal/service` или `internal/blob`, на усмотрение, но реализации не должны знать о сервисах):

```go
// Store persists immutable blob contents addressed by an opaque ID.
// Blobs are write-once: an ID is never overwritten, a replacement gets a new ID.
type Store interface {
	// Put streams r into a new blob. It enforces maxSize, computes the SHA-256
	// of the stored bytes and returns it together with the size. If expectSHA256
	// is non-empty and does not match, the blob is not kept and ErrChecksumMismatch
	// is returned. On any error no blob must remain visible under the returned ID.
	Put(ctx context.Context, id string, r io.Reader, opts PutOptions) (PutResult, error)

	// Open returns a reader for the byte range [off, off+length) of the blob.
	// length < 0 means "to the end". Implementations must honor ctx cancellation.
	Open(ctx context.Context, id string, off, length int64) (io.ReadCloser, error)

	// Delete removes the blob. Deleting a missing blob is not an error.
	Delete(ctx context.Context, id string) error

	// Walk calls fn for every stored blob ID with its size and last-modified time.
	// Used by the janitor to find orphans; must be safe to run alongside writes.
	Walk(ctx context.Context, fn func(BlobInfo) error) error

	// Check verifies the backend is reachable and writable (used by /healthz).
	Check(ctx context.Context) error
}

type PutOptions struct {
	MaxSize      int64  // hard limit; exceeding it aborts with ErrTooLarge
	SizeHint     int64  // Content-Length if known, -1 otherwise
	ContentType  string // stored as object metadata where supported
	ExpectSHA256 string // optional hex digest supplied by the client
}

type PutResult struct {
	Size   int64
	SHA256 string // lowercase hex
}
```

Плюс опциональный интерфейс отдачи (§5):

```go
// Deliverer is implemented by backends that can hand content delivery off to
// something faster than streaming through the Go process.
type Deliverer interface {
	// Deliver writes a response that makes the client receive the blob, e.g. an
	// nginx X-Accel-Redirect or an HTTP redirect to a presigned URL. It returns
	// false if delivery must fall back to streaming via Open.
	Deliver(w http.ResponseWriter, r *http.Request, id string, h DeliveryHeaders) (bool, error)
}
```

Типизированные ошибки: `ErrNotFound`, `ErrTooLarge`, `ErrChecksumMismatch`. Ошибки SDK оборачиваются, наружу (в логи) не утекают креды или подписанные URL.

Общий контрактный тест-сьют (`blobtest.Run(t, newStore)`) MUST прогоняться для обеих реализаций: put/open/delete, лимит, mismatch хэша, range-чтение, walk, отмена контекста посреди загрузки, отсутствие «полузаписанных» блобов после ошибки.

---

## 4. Реализация `fs`

Ровно то, что описано в основном SPEC (§5.2, §5.3): `tmp/` → fsync → rename в `blobs/<shard>/<id>`, права 0644/0755. `Deliverer` реализуется через `X-Accel-Redirect`, если задан `VELA_ACCEL_PREFIX`; иначе отдача стримом.

---

## 5. Реализация `r2`

### 5.1 Подключение

- Библиотека: `github.com/aws/aws-sdk-go-v2` (`service/s3`, `feature/s3/manager`).
- Эндпоинт: `https://<VELA_R2_ACCOUNT_ID>.r2.cloudflarestorage.com` (для EU-юрисдикции — `...eu.r2.cloudflarestorage.com`, задаётся через `VELA_R2_ENDPOINT`, если нужен иной).
- Регион: `auto`.
- Креды: статические (`VELA_R2_ACCESS_KEY_ID`, `VELA_R2_SECRET_ACCESS_KEY`), без цепочки AWS-провайдеров (не читать `~/.aws`, env `AWS_*`, IMDS), чтобы поведение было предсказуемым.
- Таймауты: HTTP-клиент с разумными таймаутами на соединение/заголовки, без общего таймаута на тело (большие файлы).
- Ретраи: стандартный ретраер SDK (идемпотентные операции), максимум 3 попытки.

### 5.2 Раскладка в бакете

```
<bucket>/
└── blobs/<ab>/<cd>/<blobID>     # same sharding as fs, prefix configurable via VELA_R2_PREFIX
```

- Бакет **приватный**: без публичного доступа, без `r2.dev`-поддомена, без кастомного домена на бакете. Всё доступно только через Go.
- Ключ объекта содержит только ID, никаких логических путей и имён файлов (переименование — по-прежнему только запись в БД).
- В метаданные объекта MAY писаться `Content-Type` и `x-amz-meta-sha256` — для отладки и миграций, но источник истины всегда SQLite.

### 5.3 Загрузка (Put)

По умолчанию **через сервер**: CLI → nginx → Go → R2, потоково, без промежуточного файла на диске VPS (диска мало).

- `manager.Uploader` (multipart): размер части 8–16 MiB, параллелизм 2–4 части. Память на загрузку ограничена `partSize × concurrency`; это MUST быть учтено (и задокументировано), чтобы несколько параллельных загрузок не съели RAM маленького VPS. Ограничить количество одновременных загрузок семафором (`VELA_MAX_CONCURRENT_UPLOADS`, default 4), лишние ждут или получают 429/503.
- Поток оборачивается в счётчик размера с лимитом (`MaxSize`) и `io.TeeReader` в SHA-256. Превышение лимита → отмена контекста → `AbortMultipartUpload` → `ErrTooLarge`.
- Несовпадение `ExpectSHA256` обнаруживается только после загрузки: объект удаляется (`DeleteObject`), возвращается `ErrChecksumMismatch`.
- Мелкие файлы (меньше одной части) SDK отправит одним `PutObject` — это нормально.
- При любой ошибке незавершённый multipart абортится; если процесс упал, остатки чистит janitor (§7) и lifecycle-правило бакета (§8).

Режим **presigned PUT** (клиент грузит прямо в R2) в v1 **не делается** — см. §12, вопрос 3. Интерфейсы не должны мешать добавить его позже (статус объекта `pending` + подтверждение).

### 5.4 Отдача (Open / Deliver)

Режим задаётся `VELA_R2_DELIVERY=proxy|redirect`, по умолчанию **`proxy`**.

**`proxy` (default):**
- Go делает `GetObject` и стримит тело клиенту (`io.Copy` с буфером 32–64 KiB, без чтения в память целиком).
- `Range`: Go сам парсит заголовок `Range` (поддерживается один диапазон; multi-range → отдать весь файл с 200, это допустимо по RFC), передаёт его в `GetObject`, отвечает `206` с `Content-Range`. Некорректный/неудовлетворимый диапазон → `416` с `Content-Range: bytes */<size>`.
- `HEAD` и условные запросы (`If-None-Match`, `If-Modified-Since`) обрабатываются **по данным SQLite**, без обращения к R2. `ETag: "sha256:<hex>"`, `Last-Modified: updated_at`.
- Размер и `Content-Type` берутся из БД, не из ответа R2.
- Обрыв клиента → контекст отменяется → тело `GetObject` закрывается.
- Все заголовки политики (cache, robots, referrer, nosniff, disposition) выставляет Go напрямую — как в direct-режиме основного SPEC.

**`redirect`:**
- Go проверяет доступ как обычно, затем отвечает `302` на presigned GET URL R2 с коротким сроком (`VELA_R2_PRESIGN_TTL`, default 5m, max 1h).
- В presigned URL MUST передаваться `response-content-type`, `response-content-disposition` (с `filename*`) и `response-cache-control`, чтобы браузер получил правильные заголовки от R2.
- Ответ с редиректом: `Cache-Control: private, no-store`, `Referrer-Policy: no-referrer`.
- Ограничения, которые MUST быть описаны в README: в адресной строке/загрузках появляется хост `*.r2.cloudflarestorage.com`; presigned-ссылка, однажды выданная, живёт до своего срока даже если файл сделали private (поэтому срок короткий); для `public`-файлов это работает, но публичный URL при этом не кэшируется браузером так же хорошо.
- Стоит проверить в документации Cloudflare на момент реализации, поддерживаются ли presigned URL на кастомном домене бакета; в спеке исходим из того, что **нет**.
- MAY: гибридный режим — `VELA_R2_REDIRECT_MIN_SIZE` (например, 50 MiB): файлы меньше порога проксируются, больше — редиректятся. Реализовать, если это несложно; иначе оставить на потом.

### 5.5 Удаление, Walk, Check

- `Delete` → `DeleteObject` (идемпотентно).
- `Walk` → `ListObjectsV2` с пагинацией по префиксу `blobs/`. Учитывать, что листинг в R2 — платная операция класса A: janitor вызывает его раз в сутки, этого достаточно.
- `Check` → `HeadBucket`, результат кэшируется на 30–60 секунд, чтобы `/healthz` (Docker healthcheck каждые 30s) не генерировал лишние запросы.

---

## 6. Конфигурация (дополнение к §13 основного SPEC)

| Переменная | Default | Описание |
|---|---|---|
| `VELA_STORAGE` | `fs` | `fs` или `r2` |
| `VELA_R2_ACCOUNT_ID` | — | обязательна при `r2` |
| `VELA_R2_ENDPOINT` | вычисляется из account ID | переопределение эндпоинта (EU-юрисдикция, MinIO в тестах) |
| `VELA_R2_BUCKET` | — | обязательна при `r2` |
| `VELA_R2_PREFIX` | `blobs/` | префикс ключей в бакете |
| `VELA_R2_ACCESS_KEY_ID` | — | обязательна при `r2` |
| `VELA_R2_SECRET_ACCESS_KEY` | — | обязательна при `r2`; никогда не логируется |
| `VELA_R2_DELIVERY` | `proxy` | `proxy` или `redirect` |
| `VELA_R2_PRESIGN_TTL` | `5m` | срок presigned URL в режиме `redirect` |
| `VELA_R2_PART_SIZE` | `8MiB` | размер части multipart |
| `VELA_R2_UPLOAD_CONCURRENCY` | `2` | параллельных частей на одну загрузку |
| `VELA_MAX_CONCURRENT_UPLOADS` | `4` | одновременных загрузок на сервер (для обоих бэкендов) |
| `VELA_R2_PATH_STYLE` | `true` | path-style адресация (нужно для MinIO в тестах; R2 поддерживает обе) |

Валидация при старте (fail fast):
- `VELA_ACCEL_PREFIX` допустим только с `VELA_STORAGE=fs`; при `r2` он игнорируется с предупреждением в логе (или ошибка — на усмотрение, но поведение задокументировать).
- При `r2` все обязательные переменные заданы; на старте делается `HeadBucket`, при ошибке сервер не стартует с понятным сообщением (неверные креды, нет бакета, нет сети).
- Секреты читаются из env; MAY поддержать `VELA_R2_SECRET_ACCESS_KEY_FILE` (путь к файлу) — удобно с `docker run -v`.

### 6.1 R2 API токен

В README описать создание токена в дашборде Cloudflare: **R2 → Manage API tokens → Create**, права **Object Read & Write**, ограничение **только на этот бакет**, без прав на управление аккаунтом. Токен хранится в `/etc/vela/vela.env` (права 600).

---

## 7. Janitor (изменения)

Дополнительно к §9 основного SPEC, при `r2`:

- Сиротские блобы ищутся через `Walk` (ключи есть в бакете, нет в `objects`/`trash`) и удаляются, если старше 1 часа.
- Незавершённые multipart-загрузки: `ListMultipartUploads` по префиксу, абортить начатые больше 24 часов назад.
- Очистка `tmp/` остаётся (локальные временные файлы могут использоваться в других местах, например, при `vela new`), но в R2-режиме при загрузке tmp не используется.

---

## 8. Настройка бакета (описать в README)

1. Создать бакет (например, `vela-store`), location hint — ближайший регион.
2. Public access: **выключен**, `r2.dev` — **выключен**, кастомный домен на бакет **не подключать**.
3. Lifecycle rule: «Abort incomplete multipart uploads after 1 day» — страховка на случай, если janitor не отработал.
4. Создать API-токен (§6.1).
5. Прописать переменные в env-файл, перезапустить контейнер, проверить `docker logs vela` (успешный `HeadBucket`) и `vela whoami`.

---

## 9. Миграция данных между бэкендами

Команда:

```
docker exec vela velad storage migrate --from fs --to r2 [--dry-run] [--delete-source]
```

- Читает конфиг обоих бэкендов (для `fs` — `VELA_DATA_DIR`, для `r2` — `VELA_R2_*`), независимо от текущего `VELA_STORAGE`.
- Копирует все блобы, на которые ссылаются `objects` и `trash`, сверяет SHA-256 после копирования, пропускает уже существующие (идемпотентна, можно перезапускать после обрыва).
- Печатает прогресс и итог (скопировано / пропущено / ошибок).
- Не меняет БД (ID блобов одинаковы в обоих бэкендах).
- `--delete-source` удаляет исходные блобы только после успешной проверки всех.
- Порядок переключения (в README): остановить запись (или просто не загружать ничего) → `migrate` → поменять `VELA_STORAGE=r2` → перезапуск → проверка → при желании повторный `migrate --delete-source`.
- Обратное направление `--from r2 --to fs` MUST работать так же.

---

## 10. nginx (изменения)

При `VELA_STORAGE=r2`:

- Location `/_blobs/` не нужна (её можно оставить в конфиге — она internal и без accel-ответов никогда не сработает, но в референсном конфиге показать оба варианта с комментариями).
- Для публичной location `/` при проксирующей отдаче MUST быть `proxy_buffering off;` (или `X-Accel-Buffering: no` от Go на ответах с содержимым), чтобы большие файлы не буферизовались nginx-ом на маленький диск VPS (`proxy_temp`), и разумный `proxy_read_timeout` (например, 600s).
- `proxy_max_temp_file_size 0;` как дополнительная гарантия, что nginx не пишет тела ответов на диск.
- MAY (вне v1): `proxy_cache` для public-файлов на VPS. С учётом диска в 1 GB — не включать по умолчанию.

---

## 11. Тесты и CI (дополнение)

- Контрактный сьют §3 гоняется:
  - для `fs` — всегда;
  - для `r2` — против **MinIO** в CI (`services:` в GitHub Actions, образ `minio/minio`), через `VELA_R2_ENDPOINT` + `VELA_R2_PATH_STYLE=true`. Локально — `make test-s3` (поднимает MinIO через `docker run`, без compose).
- HTTP-тесты отдачи в режиме `proxy`: полный файл, `Range` (начало, середина, `bytes=-N`), `416`, `HEAD`, `If-None-Match` → `304`, обрыв клиента.
- Тест режима `redirect`: ответ `302`, в URL присутствуют `response-content-disposition` и `X-Amz-Expires` ≤ настроенного TTL.
- Тест `storage migrate` fs → s3(MinIO) → fs с проверкой хэшей.
- Опционально (ручной чек-лист в README): smoke-тест против реального R2-бакета.

---

## 12. Открытые вопросы

1. **Трафик VPS**: какой лимит исходящего трафика у провайдера? В режиме `proxy` весь трафик скачиваний идёт через VPS. Если лимит маленький или платный — стоит сделать `redirect` (или гибрид по размеру) режимом по умолчанию.
2. **RAM VPS**: сколько памяти? От этого зависят дефолты `VELA_R2_PART_SIZE`, `VELA_R2_UPLOAD_CONCURRENCY` и `VELA_MAX_CONCURRENT_UPLOADS`.
3. **Presigned PUT (загрузка напрямую в R2)** нужен в v1? Он разгружает VPS и снимает лимит `client_max_body_size`, но требует статуса `pending` у объекта, эндпоинта подтверждения и чистки брошенных загрузок. Предлагаю: нет в v1, интерфейсы готовы к нему.
4. **Гибридный режим отдачи** по порогу размера — делать сразу или потом?
5. **Юрисдикция бакета**: обычная или EU?
6. **`VELA_ACCEL_PREFIX` при `r2`**: предупреждение или ошибка при старте?
7. **Бэкап**: достаточно `velad backup` (только БД) плюс инструкция с `rclone sync` для бакета, или нужна встроенная команда выгрузки блобов из R2?

---

## 13. Критерии готовности (дополнение к §20 основного SPEC)

- [ ] `VELA_STORAGE=fs` и `VELA_STORAGE=r2` работают одним и тем же образом; CLI не зависит от S3 SDK.
- [ ] Контрактный сьют проходит для `fs` и для S3 (MinIO в CI).
- [ ] Загрузка в R2 потоковая, без промежуточного файла на диске, с лимитом размера, проверкой SHA-256 и абортом multipart при ошибке; память ограничена конфигом.
- [ ] Отдача в режиме `proxy` поддерживает `Range`, `HEAD`, условные запросы; nginx не буферизует тела на диск.
- [ ] Режим `redirect` отдаёт короткоживущие presigned URL с правильными `Content-Type`/`Content-Disposition`.
- [ ] Janitor чистит сиротские объекты и незавершённые multipart-загрузки в R2.
- [ ] `velad storage migrate` переносит данные в обе стороны идемпотентно, с проверкой хэшей.
- [ ] README описывает настройку бакета, токена, переменных, переключение бэкендов и миграцию.

## 14. Реализация и принятые решения (2026-10-11)

- Используются заданные в этом документе настройки по умолчанию. Presigned PUT и гибридная отдача не включены; EU выбирается через endpoint. При R2 непустой accel-prefix — ошибка запуска. R2 backup сохраняет только БД, бакет резервируется отдельно (см. README).
- Контракт находится в `internal/content`; `internal/blob` и `internal/r2` не импортируют сервис. Возможность presign передаётся через сервис без HTTP-интерфейса в storage. Последние четыре случайных символа ID задают shard в обоих бэкендах.
- Размер буферов SDK включает дополнительную часть чтения вперёд: `partSize × (concurrency + 1) × maxConcurrentUploads`, около 96 MiB при дефолтах плюс накладные расходы.
- SHA-256 ETag остаётся единственным валидатором; Last-Modified/If-Modified-Since не включены из-за уже исправленного R-013. HEAD и 304 используют только SQLite.
- HeadBucket кэшируется 45 секунд и проверяет доступность/доступ к бакету; права записи проверяются smoke-загрузкой.
- Контракт, HTTP-доставка и миграция проверены на MinIO. CI собирает закреплённый MinIO из исходников и запускает его в Docker; публичные образы при реализации были недоступны. Для реального R2 остаётся ручной smoke-тест из README.
- Все отклонения записаны в `docs/SPEC.md`, §21; инструкция настройки, миграции и резервного копирования — в README.
