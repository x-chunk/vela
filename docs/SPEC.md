# Vela — спецификация (SPEC)

> Design specification for Vela. MUST / SHOULD / MAY describe the contract; section 21 records implementation decisions. Contributor guidance lives in AGENTS.md.

---

## 0. TL;DR

**Vela** — self-hosted файловое хранилище с REST API, CLI и публичными URL:

- **Сервер `velad`**: standalone Go-бинарник в одном Docker-контейнере (без docker compose). REST API `/api/v1` с аутентификацией по ключу в заголовке, публичная раздача файлов, красивый (кастомизируемый) HTML-индекс, подписанные ссылки с TTL для приватных файлов, корзина, ежедневная фоновая чистка.
- **CLI `vela`**: standalone бинарник для управления хранилищем с компьютера: создание файлов из сырого контента, загрузка, удаление, переименование/перемещение, управление метаданными и видимостью, выдача подписанных ссылок.
- **Delivery**: Go streams files directly by default, with or without a reverse proxy. nginx accel is an explicit filesystem optimization; all access decisions remain in Go. TLS and reverse proxy configuration belong to the operator.
- **Монорепо**, один Go-модуль `go.xchunk.org/vela`, общий код (контракт API, валидация путей, клиент) используется и сервером, и CLI.
- **CI/CD**: GitHub Actions собирает образ и пушит в **публичный** Docker Hub (`xchunk/vela`). На сервере образ пуллится вручную. *(Изменено, см. §21.)*

### Проблема, которую решаем

Vela consolidates ad-hoc static file hosting behind one API. One service owns file contents, metadata, permissions, expiring links, and cleanup, while the CLI supports interactive and automated workflows.

---

## 1. Цели и не-цели

### Цели (v1)

1. Единое хранилище с тремя уровнями видимости: **public** (в индексе), **unlisted** (доступен по прямому URL, но не в индексе), **private** (только через API-ключ или подписанную ссылку).
2. Операции: создание из сырого контента, потоковая загрузка (любые размеры в пределах лимита), скачивание, листинг, stat, перемещение/переименование (в т.ч. рекурсивно по префиксу), удаление в корзину, восстановление, окончательное удаление, управление метаданными.
3. Подписанные ссылки с TTL, зашитым в ссылку (HMAC), с возможностью отзыва.
4. Ежедневная серверная чистка: просроченные записи ссылок, просроченные файлы, старая корзина, сиротские блобы и временные файлы.
5. Кастомный HTML-индекс, рендерится Go.
6. Ноль ручной возни с правами на сервере после первичной настройки.
7. Хорошая архитектура, комментарии production-уровня, компактный README с Getting Started и технические справки (§18, этап 4).

### Не-цели (v1)

- Пользователи, роли, ACL уровня «многопользовательского облака». Есть только API-ключи со scope и префиксом (§7).
- Веб-UI для управления (загрузка из браузера и т.п.). Управление только через API/CLI.
- Версионирование содержимого файлов, дедупликация, превью, миниатюры, поиск по содержимому.
- docker compose, Kubernetes, внешние БД.
- Deployment-specific import tools; integrations can use scoped API keys and the public Go client.

---

## 2. Термины

| Термин | Значение |
|---|---|
| **Object (объект)** | Логический файл: путь + метаданные + ссылка на блоб. |
| **Path (путь)** | Логический путь объекта, например `docs/math/q1/test.pdf`. Без ведущего `/`. Регистрозависимый. |
| **Directory (директория)** | Неявная: существует, пока под префиксом `dir/` есть хотя бы один объект. Пустых директорий нет. |
| **Blob** | Файл с содержимым на диске, имя — случайный ID, не связано с логическим путём. |
| **Visibility** | `public` / `unlisted` / `private`. |
| **API key** | Секрет для доступа к `/api/v1`. Имеет имя, scope и опциональный префикс путей. |
| **Signed link** | Ссылка вида `/s/{linkID}/{filename}?exp=...&sig=...`, дающая доступ к одному объекту до момента `exp`. |
| **Trash (корзина)** | Удалённые объекты, восстанавливаемые до истечения retention. |
| **Janitor** | Фоновая задача чистки. |

---

## 3. Модель видимости

| | В HTML-индексе | Прямой URL `https://store.example.com/<path>` | Через API с ключом | Через подписанную ссылку |
|---|---|---|---|---|
| `public` | да | да | да | да |
| `unlisted` | **нет** | да | да | да |
| `private` | **нет** | **нет (404)** | да | да |

Правила (**MUST**):

- Запрос private-объекта по прямому URL возвращает **404**, а не 403: существование приватного файла не раскрывается.
- Unlisted и private-объекты не влияют на индекс: директория, в которой нет ни одного public-потомка, в индексе не отображается, а её индекс-страница отдаёт 404.
- Смена видимости вступает в силу **немедленно**: если объект стал private, его публичный URL сразу отдаёт 404.
- Unlisted-ответы содержат `X-Robots-Tag: noindex, nofollow`.
- Ответы по подписанным ссылкам содержат `Cache-Control: private, no-store`, `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex, nofollow`.
- «Unlisted» — это **не защита**, а просто отсутствие в индексе. Это должно быть явно написано в README.
- Видимость по умолчанию для новых объектов: **`unlisted`** (`VELA_DEFAULT_VISIBILITY`, решение по §19.5). CLI MAY позволять задать иной дефолт в своём конфиге (§12.2).

---

## 4. Пути

### 4.1 Правила валидации (**MUST**, общий код в `api/`, используется и сервером, и CLI)

- Строка в UTF-8, нормализуется в **Unicode NFC** (`golang.org/x/text/unicode/norm`).
- Ведущий `/` в пользовательском вводе срезается (CLI и сервер принимают `/docs/a.pdf` как `docs/a.pdf`). Хранится без ведущего `/`.
- Объект не может заканчиваться на `/`. Завершающий `/` в операциях листинга/перемещения/удаления означает «директорию/префикс».
- Нет пустых сегментов (`a//b`), нет сегментов `.` и `..`.
- Запрещены: `\`, NUL и все управляющие символы (U+0000–U+001F, U+007F), а также сегменты, состоящие только из пробелов. Ведущие/завершающие пробелы в сегменте запрещены.
- Длина пути ≤ 1024 байт, сегмента ≤ 255 байт.
- **Зарезервированные первые сегменты** (регистронезависимо): `api`, `s`, `_vela`, `_blobs`, `healthz`, `robots.txt`, `favicon.ico`. Объекты с таким первым сегментом создать нельзя.
- Регистрозависимо: `A.pdf` и `a.pdf` — разные объекты.

### 4.2 Конфликт файл/директория (**MUST**)

Как в файловой системе, путь не может быть одновременно файлом и директорией:

- Нельзя создать `a/b`, если существует объект `a`.
- Нельзя создать `a`, если существует хоть один объект с префиксом `a/`.
- Эти проверки действуют для create, upload, move и restore. Нарушение → `409 conflict` с кодом `path_conflict`.

### 4.3 URL-кодирование

- В URL каждый сегмент кодируется отдельно (`url.PathEscape` на сегмент), `/` между сегментами не кодируется.
- Публичный URL объекта: `{VELA_PUBLIC_URL}/{escaped path}`.

---

## 5. Архитектура

### 5.1 Общая схема

```
             HTTPS                       HTTP (127.0.0.1:8080)
client ───────────────► nginx ──────────────────────────────► velad (Docker)
(browser / vela CLI)      │                                     │
                          │  ◄── X-Accel-Redirect: /_blobs/... ─┘
                          │
                          └── internal location /_blobs/ → /srv/vela/blobs/ (read-only)
```

1. **Все** запросы (кроме internal `/_blobs/`) nginx проксирует в `velad`.
2. `velad` решает: что это за путь, какой объект, можно ли его отдать, с какими заголовками.
3. Если нужно отдать содержимое файла, `velad` отвечает пустым телом с заголовком `X-Accel-Redirect: /_blobs/<shard>/<blobID>` и нужными заголовками (`Content-Type`, `Content-Disposition`, служебные `X-Vela-*`). nginx делает internal redirect и отдаёт файл сам: эффективно, с поддержкой `Range`, `HEAD`, `sendfile`.
4. **Режим без nginx** (локальная разработка, тесты): если `VELA_ACCEL_PREFIX` пуст, `velad` отдаёт содержимое сам через `http.ServeContent` (Range/HEAD/If-Modified-Since поддерживаются стандартной библиотекой). Логика доступа одинакова в обоих режимах: различается только финальный шаг отдачи байтов.

### 5.2 Хранилище на диске

Внутри контейнера всё в `VELA_DATA_DIR` (по умолчанию `/data`), на хосте это bind-mount `/srv/vela`:

```
/data
├── vela.db            # SQLite (WAL), 0600
├── vela.db-wal / -shm
├── blobs/             # 0755; содержимое файлов, читается nginx-ом
│   └── ab/cd/<blobID> # файлы 0644, шардирование по ПОСЛЕДНИМ 4 символам ID (см. §21)
├── tmp/               # 0700; временные файлы загрузки (тот же FS, что blobs, для атомарного rename)
└── secrets/           # 0700
    └── (опционально: ничего, если ключи подписи в БД; см. §8.4)
```

- Блобы адресуются **случайным ID** (ULID, lower-case), не путём и не хэшем. Переименование и перемещение — это **только обновление БД**, без файловых операций: атомарно и мгновенно.
- Дедупликации нет: каждая загрузка создаёт новый блоб.
- `tmp/` MUST находиться на той же файловой системе, что и `blobs/` (атомарный `rename(2)`).
- Права: процесс в контейнере работает от фиксированного uid (distroless `nonroot`, uid/gid `65532`). `blobs/` и файлы внутри доступны на чтение всем (0755/0644), чтобы nginx (`www-data`) мог их читать. БД и `tmp/` — только владельцу. Сервер MUST выставлять эти права явно при создании (не полагаться на umask).

### 5.3 Атомарность записи (**MUST**)

Загрузка/создание:

1. Поток пишется во временный файл в `tmp/`, параллельно считаются SHA-256 и размер. Превышение лимита размера → немедленный обрыв, удаление tmp, `413`.
2. Если клиент прислал `X-Vela-SHA256`, хэш сверяется → при несовпадении `400 checksum_mismatch`.
3. `fsync` файла, `chmod 0644`, `rename` в `blobs/<shard>/<blobID>`, `fsync` директории.
4. Транзакция БД: проверка конфликтов (§4.2, If-Match/If-None-Match), insert/update объекта.
5. При ошибке шага 4 новый блоб удаляется. При перезаписи **старый** блоб удаляется только после успешного коммита.
6. Если процесс упал посреди операции, остатки подберёт janitor (сиротские блобы и старые tmp-файлы, §9).

### 5.4 Слои (clean architecture)

```
transport (HTTP handlers, middleware)       internal/server
        │
        ▼
service (business logic, invariants)        internal/service
        │
        ├──► repository interfaces ─────► internal/store   (SQLite implementation)
        ├──► blob storage interface ────► internal/blob    (filesystem implementation)
        └──► signer, clock, id generator ► internal/sign, internal/ids
```

- Handlers не знают про SQL и файловую систему; сервисы не знают про HTTP.
- Сервисы зависят от **интерфейсов** (определены на стороне потребителя, в `service`), реализации инжектятся в `cmd/velad`.
- `Clock` абстрагирован (для тестов TTL/janitor).
- Ошибки домена — типизированные (`service.ErrNotFound`, `ErrConflict`, …), в HTTP-коды и коды контракта (§10.3) маппятся в одном месте в `internal/server`.

---

## 6. Структура монорепо

Один модуль: `go.xchunk.org/vela`. **Не** go.work: сервер и CLI всегда собираются из одного коммита, общий код — это контракт между ними, отдельное версионирование ему не нужно.

```
vela/
├── go.mod                      # module go.xchunk.org/vela
├── go.sum
├── Makefile
├── Dockerfile
├── .dockerignore
├── .golangci.yml
├── AGENTS.md                   # instructions for review agents (Astra) and coding agents
├── README.md                   # overview + Getting Started (stage 4)
├── docs/
│   └── SPEC.md                 # this document
├── .github/workflows/
│   ├── ci.yml
│   └── release.yml
├── cmd/
│   ├── vela/main.go            # CLI entrypoint (thin: wires internal/cli)
│   └── velad/main.go           # server entrypoint (thin: wires everything)
├── api/                        # PUBLIC, shared: the API contract
│   ├── doc.go
│   ├── types.go                # DTOs: Object, Listing, Link, TrashEntry, Info, requests
│   ├── errors.go               # error codes, Error type, (un)marshaling
│   ├── visibility.go           # Visibility enum + parsing
│   ├── path.go                 # path normalization + validation (§4)
│   ├── duration.go             # human durations: 90m, 2h, 7d, 2w
│   ├── headers.go              # header names (X-Vela-*), route constants
│   └── *_test.go
├── client/                     # PUBLIC, shared: Go client for /api/v1
│   ├── client.go               # New(baseURL, key, opts...), retries, UA, version check
│   ├── objects.go
│   ├── links.go
│   ├── trash.go
│   └── *_test.go               # tested against an in-process velad (httptest)
├── internal/
│   ├── version/                # Version/Commit/Date via ldflags, fallback to debug.ReadBuildInfo
│   ├── keys/                   # API key format: generate, parse, hash (shared by velad key cmds)
│   ├── config/                 # server config (env + flags), validation
│   ├── ids/                    # ULID generation behind an interface
│   ├── blob/                   # filesystem blob store: Put (stream->tmp->rename), Open, Delete, Walk
│   ├── store/                  # SQLite repositories, embedded migrations
│   │   └── migrations/*.sql
│   ├── sign/                   # HMAC signed links, signing key ring
│   ├── service/                # objects, links, trash, keys, janitor
│   ├── index/                  # HTML index rendering, embedded default theme, theme override
│   │   └── theme/              # layout.html, index.html, error.html, assets/
│   ├── server/                 # router, handlers, middleware (auth, logging, request ID, rate limit,
│   │                           # real IP, recover), error mapping, content delivery (accel/direct)
│   ├── cli/                    # cobra commands for `vela`
│   │   ├── config/             # CLI config file (profiles, permissions check)
│   │   └── output/             # human/JSON output, tables, sizes, progress
│   ├── testutil/               # helpers to spin up velad in tests
│   │   # added during implementation (see §21):
│   ├── domain/                 # entities shared by service and store
│   ├── wire/                   # assembles service from store + blob (adapters)
│   ├── velad/                  # velad subcommands (serve, key, signing, ...)
│   ├── mimetype/               # content type detection
│   ├── clock/, units/          # injectable clock; byte sizes
│   └── archtest/               # test enforcing the import rules between layers
├── examples/
│   ├── nginx/store.example.com.conf   # reference nginx config (§14)
│   ├── nginx/vela.conf               # locations, included by the above and by test/e2e
│   └── vela.env.example              # reference env file for the container
└── test/e2e/                         # accel delivery through a real nginx (build tag e2e)
```

Почему `api/` и `client/` публичные: чтобы любой инструмент автоматизации мог импортировать `go.xchunk.org/vela/client`, а не дёргать CLI. Всё остальное в `internal/`.

---

## 7. Аутентификация и ключи

### 7.1 Формат ключа

```
vela_<keyID>_<secret>
```

- `keyID`: 10 символов, base32 lower-case (Crockford или RFC 4648 без паддинга), случайный, неуникальность проверяется при генерации.
- `secret`: 32 случайных байта (`crypto/rand`), base64url без паддинга.
- Префикс `vela_` нужен, чтобы ключ легко находился сканерами секретов (GitHub secret scanning и т.п.) и грепом.

### 7.2 Хранение на сервере

- В БД хранится только `SHA-256(secret)` (секрет высокоэнтропийный, поэтому медленный KDF вроде argon2 не нужен).
- Проверка: найти запись по `keyID`, сравнить хэши через `crypto/subtle.ConstantTimeCompare`, проверить `revoked_at IS NULL`.
- `last_used_at` обновляется не чаще раза в минуту на ключ (throttle в памяти), чтобы не писать в БД на каждый запрос.
- Полный ключ показывается **один раз** при создании.

### 7.3 Передача

`Authorization: Bearer vela_<keyID>_<secret>`. Других способов (query-параметр, cookie) **нет**: ключ не должен попадать в логи nginx и историю браузера.

### 7.4 Scope и префикс

| Scope | Права |
|---|---|
| `ro` | list, stat, download через API, `GET /api/v1/info` |
| `rw` | всё из `ro` + create, upload, move, delete в корзину, restore, metadata, создание/отзыв ссылок, просмотр корзины |
| `admin` | всё из `rw` + purge (окончательное удаление), очистка корзины целиком, работа без ограничения префиксом |

- Опциональный **префикс** (`--prefix docs/`): ключ видит и меняет только объекты с этим префиксом. Перемещение из префикса или в префикс за его пределами → `403 forbidden`. Листинг автоматически ограничивается префиксом.
- Use case: отдельный ключ `rw` с префиксом `ci/` для автоматизации, чтобы компрометация не задела остальное.
- Ключ `admin` с префиксом допустим (admin-права внутри префикса).

### 7.5 Управление ключами

**Только** через серверную CLI-подкоманду внутри контейнера, не через HTTP API (нет пути эскалации привилегий через API):

```
docker exec vela velad key create --name workstation --scope admin
docker exec vela velad key create --name ci --scope rw --prefix ci/
docker exec vela velad key list
docker exec vela velad key revoke <keyID>
```

`velad key create` печатает полный ключ один раз. `key list` показывает ID, имя, scope, префикс, created_at, last_used_at, revoked_at — без секрета.

### 7.6 Защита от перебора

- Rate limit на **неудачные** попытки аутентификации по IP: token bucket в памяти (например, 10 неудач в минуту, burst 10), после превышения `429 rate_limited` с `Retry-After`.
- Реальный IP клиента берётся из `X-Real-IP` / `X-Forwarded-For` **только** если запрос пришёл от доверенного прокси (`VELA_TRUSTED_PROXIES`, по умолчанию `127.0.0.1/32,::1/128,172.17.0.1/32` — только шлюз docker bridge, решение по §19.14). Иначе — `RemoteAddr`.
- Ответ на неверный ключ всегда одинаковый (`401 unauthorized`, без уточнения «нет такого ID» или «неверный секрет»).

---

## 8. Подписанные ссылки и TTL

Решение принято: **TTL зашит в ссылку (HMAC)**, а сервер дополнительно хранит запись о ссылке (для листинга и отзыва) и **раз в сутки** чистит просроченное.

### 8.1 Формат URL

```
https://store.example.com/s/{linkID}/{filename}?exp={unix_seconds}&sig={signature}
```

- `linkID`: ULID записи в таблице `links`.
- `filename`: косметический (имя файла на момент создания ссылки, URL-encoded), чтобы ссылка была читаемой и браузер предлагал правильное имя. В проверке **не участвует**.
- `exp`: Unix-время истечения, секунды.
- `sig`: `base64url(HMAC-SHA256(signingKey, "vela-link-v1\n" + linkID + "\n" + exp))`, без паддинга. Можно усечь до 16 байт (128 бит), этого достаточно.
- Подпись включает ID ключа подписи косвенно: запись `links` хранит `signing_key_id` (§8.4).

### 8.2 Проверка (порядок MUST)

1. Распарсить `exp`; если `now > exp` → **410 Gone** (явная страница «ссылка истекла»). Это работает **без** обращения к БД.
2. Найти запись `links` по `linkID`. Нет записи → 404.
3. Проверить подпись с ключом `signing_key_id` из записи, `subtle.ConstantTimeCompare`. Не совпадает → 404. `exp` в URL MUST совпадать с `expires_at` записи (иначе 404): нельзя продлить ссылку подменой `exp`.
4. Запись отозвана (`revoked_at` не NULL) → 404.
5. Объект не существует, в корзине или его собственный `expires_at` прошёл → 404.
6. Отдать содержимое (§11.3) с заголовками из §3.

### 8.3 Поведение

- Ссылка привязана к **объекту (object ID), а не к пути**: после переименования или перемещения ссылка продолжает работать.
- Удаление объекта в корзину делает ссылку неработающей; восстановление из корзины возвращает работоспособность (если `exp` не истёк). Окончательное удаление (purge) каскадно удаляет записи ссылок.
- Ссылку можно создать на объект любой видимости (для public она просто избыточна, но не запрещена).
- TTL ссылки: по умолчанию `VELA_LINK_DEFAULT_TTL` (24h), максимум `VELA_LINK_MAX_TTL` (30d). Запрос больше максимума → `400 invalid_request` (не молча обрезать).
- Ссылки без срока действия **не поддерживаются** (это осознанное решение: для бессрочного доступа есть unlisted).
- Отзыв: `DELETE /api/v1/links/{id}` выставляет `revoked_at`, ссылка перестаёт работать немедленно.

### 8.4 Ключи подписи

- Хранятся в БД (`signing_keys`: id, secret 32 байта, created_at, retired_at). Таблица с секретами; файл БД имеет права 0600, это приемлемо для v1.
- При первом старте, если активного ключа нет, сервер генерирует его.
- Ротация: `velad signing rotate` создаёт новый активный ключ; старый помечается `retired_at` и продолжает проверять уже выданные ссылки. Janitor удаляет retired-ключи, у которых не осталось непросроченных ссылок.
- Аварийный отзыв всех ссылок: `velad signing rotate --revoke-all` (старый ключ удаляется сразу, все его ссылки становятся невалидными).

### 8.5 TTL объектов

Помимо TTL ссылок у объекта есть опциональное поле `expires_at` («самоуничтожающийся файл», удобно для временного мусора):

- Проверяется **при каждой отдаче**: просроченный объект сразу отдаёт 404 по публичному URL и по подписанным ссылкам и пропадает из индекса. Через API он остаётся доступен до прохода janitor: `stat` показывает его с флагом `expired: true`, скачать можно, чтобы успеть спасти (решение по §19.6).
- Физически переносится в корзину janitor-ом при ежедневном проходе.
- Задаётся при создании/загрузке или через метаданные; `null` снимает срок.

---

## 9. Janitor (фоновая чистка)

- Запускается внутри `velad` горутиной: первый проход через 1 минуту после старта, далее каждые `VELA_JANITOR_INTERVAL` (по умолчанию **24h**).
- Ручной запуск: `docker exec vela velad janitor run` (через ту же БД; должен корректно сосуществовать с запущенным сервером, SQLite WAL + busy_timeout).
- Задачи прохода:
  1. Удалить записи `links`, у которых `expires_at < now - VELA_LINK_RETENTION` (по умолчанию 7d: недавно истёкшие видны в `vela links ls --all` для отладки).
  2. Перенести в корзину объекты с `expires_at < now`.
  3. Окончательно удалить из корзины записи старше `VELA_TRASH_RETENTION` (по умолчанию 14d): строку из БД, затем блоб.
  4. Удалить retired-ключи подписи без живых ссылок.
  5. Удалить файлы в `tmp/` старше 1 часа.
  6. Найти сиротские блобы (есть на диске, нет в `objects`/`trash`) старше 1 часа и удалить их.
- Каждая задача логирует итог (количество, длительность) одной структурированной строкой. Ошибка одной задачи не прерывает остальные.
- Проход защищён от параллельного запуска (mutex в процессе + в случае ручного запуска advisory-блокировка через строку в таблице `meta` или файл-lock в `data/`).

---

## 10. HTTP API v1 (контракт)

### 10.1 Общее

- Базовый путь: `/api/v1`. Все ответы API — `application/json; charset=utf-8`, кроме скачивания содержимого.
- Время — RFC 3339 в UTC (`2026-10-10T12:00:00Z`).
- Размеры — байты (int64).
- Каждый ответ содержит `X-Request-ID` (генерируется сервером или берётся из входящего, если от доверенного прокси).
- Все эндпоинты `/api/v1/*` требуют `Authorization` (кроме нет исключений). `GET /healthz` без авторизации и не под `/api`.
- CLI шлёт `User-Agent: vela-cli/<version> (<os>/<arch>)`.
- Пути объектов передаются в URL как `{path...}` (Go 1.22+ ServeMux wildcard), каждый сегмент URL-encoded.

### 10.2 Объект (DTO)

```json
{
  "id": "01jabcdxyz...",
  "path": "docs/math/q1/test.pdf",
  "name": "test.pdf",
  "size": 482193,
  "sha256": "9f86d081884c7d65...",
  "content_type": "application/pdf",
  "disposition": "inline",
  "visibility": "public",
  "title": "Контрольная Q1",
  "description": "Вариант 2",
  "tags": ["math", "q1"],
  "created_at": "2026-10-10T12:00:00Z",
  "updated_at": "2026-10-10T12:05:00Z",
  "expires_at": null,
  "expired": false,
  "revision": 3,
  "url": "https://store.example.com/docs/math/q1/test.pdf"
}
```

- `url`: `null` для private.
- `revision`: монотонный счётчик изменений объекта (содержимое или метаданные). Используется для оптимистичной блокировки: заголовок `ETag: "r3"` в ответах API, `If-Match: "r3"` в запросах.
- `disposition`: `inline` | `attachment` → заголовок `Content-Disposition` при отдаче (с `filename*=UTF-8''...` по RFC 6266).
- `tags`: до 32 тегов, каждый ≤ 64 символа, `[a-z0-9-_.]` после lower-case. `title` ≤ 256 символов, `description` ≤ 4096.
- `content_type`: если не задан клиентом, определяется по расширению (`mime.TypeByExtension` + встроенная таблица распространённых типов), затем `http.DetectContentType` по первым 512 байтам, иначе `application/octet-stream`. Для текстовых типов добавляется `; charset=utf-8`, если не указан.

### 10.3 Ошибки

```json
{
  "error": {
    "code": "path_conflict",
    "message": "a file exists at a parent path: docs/math",
    "details": { "conflicting_path": "docs/math" },
    "request_id": "01jab..."
  }
}
```

| HTTP | `code` | Когда |
|---|---|---|
| 400 | `invalid_request` | невалидный JSON, параметры, TTL больше максимума и т.п. |
| 400 | `invalid_path` | путь не прошёл валидацию §4.1 (в `details.reason`) |
| 400 | `checksum_mismatch` | не совпал `X-Vela-SHA256` |
| 401 | `unauthorized` | нет/неверный/отозванный ключ |
| 403 | `forbidden` | не хватает scope или путь вне префикса ключа |
| 404 | `not_found` | объект/ссылка/запись корзины не найдены |
| 409 | `already_exists` | объект уже существует (create, upload с `If-None-Match: *`, move без overwrite) |
| 409 | `path_conflict` | конфликт файл/директория §4.2 |
| 412 | `precondition_failed` | не совпал `If-Match` |
| 413 | `payload_too_large` | превышен лимит размера |
| 415 | `unsupported_encoding` | create с неизвестным `encoding` |
| 429 | `rate_limited` | §7.6 |
| 500 | `internal` | всё остальное (детали только в логах) |
| 503 | `unavailable` | shutdown в процессе, БД недоступна |

Коды — константы в `api/errors.go`; клиент и CLI маппят их в типизированные ошибки и коды выхода (§12.5).

### 10.4 Эндпоинты

#### `GET /healthz` (без auth)
`200 {"status":"ok"}`, если БД отвечает и `blobs/` доступна на запись; иначе `503`.

#### `GET /api/v1/info`
```json
{
  "server_version": "1.0.0",
  "api_version": 1,
  "public_url": "https://store.example.com",
  "key": { "id": "k3f9...", "name": "workstation", "scope": "admin", "prefix": "" },
  "limits": {
    "max_upload_bytes": 2147483648,
    "max_create_bytes": 1048576,
    "link_default_ttl": "24h",
    "link_max_ttl": "720h"
  }
}
```
CLI вызывает его в `vela login`, `vela whoami`, `vela version` и при несовместимости `api_version` выдаёт понятную ошибку.

#### `GET /api/v1/objects` — листинг
Query: `prefix` (директория, с завершающим `/` или пустой для корня), `recursive` (bool, default false), `visibility` (фильтр, можно несколько через запятую), `limit` (1–1000, default 200), `cursor` (непрозрачный).

```json
{
  "prefix": "docs/",
  "dirs": ["docs/math/", "docs/physics/"],
  "objects": [ { ...Object } ],
  "next_cursor": ""
}
```
- Нерекурсивный режим возвращает непосредственных потомков: объекты и «директории» (common prefixes). Рекурсивный — только объекты, `dirs` пуст.
- Сортировка по `path` (байтовая). Пагинация курсором (последний path), стабильна.
- Ключ с префиксом видит только свой префикс; запрос за его пределами → 403.

#### `GET /api/v1/objects/{path...}` — stat
`200` Object + `ETag: "r<revision>"`.

#### `POST /api/v1/objects` — **создание из сырого контента**
Для небольших текстовых (или base64) файлов: заметки, конфиги, md. Создаёт **только новый** объект.

```json
{
  "path": "notes/todo.md",
  "content": "# hello\n",
  "encoding": "utf-8",
  "content_type": "text/markdown",
  "visibility": "unlisted",
  "title": "", "description": "", "tags": [],
  "disposition": "inline",
  "expires_at": null
}
```
- `encoding`: `utf-8` (default) или `base64`.
- Декодированный размер ≤ `VELA_MAX_CREATE` (default 1 MiB), иначе 413.
- Существует → `409 already_exists`. Ответ `201` + Object + `Location` (API URL объекта).

#### `PUT /api/v1/content/{path...}` — **загрузка** (потоковая)
Тело — сырые байты файла (не multipart).

Заголовки запроса:

| Заголовок | Значение |
|---|---|
| `Content-Type` | MIME-тип; если отсутствует или `application/octet-stream`, сервер определяет сам (§10.2) |
| `Content-Length` | SHOULD (для проверки лимита до чтения); при chunked лимит проверяется по ходу |
| `X-Vela-Visibility` | `public` / `unlisted` / `private` |
| `X-Vela-Title`, `X-Vela-Description` | percent-encoded UTF-8 |
| `X-Vela-Tags` | через запятую |
| `X-Vela-Disposition` | `inline` / `attachment` |
| `X-Vela-Expires-At` | RFC 3339 или `none` |
| `X-Vela-SHA256` | hex, опциональная проверка целостности |
| `If-None-Match: *` | только создать; если существует → 409 |
| `If-Match: "r<rev>"` | перезаписать, только если ревизия совпадает → иначе 412 |

Семантика:
- Нет объекта → создаётся (`201`). Метаданные из заголовков, остальное по умолчанию.
- Есть объект и нет `If-None-Match` → **содержимое перезаписывается** (`200`), `revision++`, `id` и не указанные в заголовках метаданные **сохраняются** (в т.ч. видимость и ссылки продолжают работать). `content_type` пересчитывается, если не передан явно.
- nginx MUST передавать тело потоково (`proxy_request_buffering off`), сервер читает `r.Body` потоком, без буферизации в память.

#### `GET /api/v1/content/{path...}` (и `HEAD`) — скачивание через API
Отдаёт содержимое любой видимости (в пределах scope/префикса ключа). Через accel или напрямую (§11.3). `ETag` для содержимого — `"sha256:<hex>"` в direct-режиме; в accel-режиме ETag формирует nginx.

#### `PATCH /api/v1/objects/{path...}` — метаданные
JSON Merge Patch (RFC 7396). Разрешённые поля: `visibility`, `title`, `description`, `tags`, `content_type`, `disposition`, `expires_at`. `null` сбрасывает поле к дефолту (`expires_at: null` снимает срок). Путь через PATCH **не меняется** (для этого move). Опционально `If-Match`. Ответ: `200` Object.

#### `POST /api/v1/move` — переименование/перемещение
```json
{ "from": "docs/math/old.pdf", "to": "docs/math/new.pdf", "overwrite": false, "recursive": false }
```
- Одиночный объект: `from` и `to` — пути объектов. Если `to` занят: без `overwrite` → 409; с `overwrite` → старый объект по `to` уходит в корзину, затем перемещение.
- Рекурсивно: `recursive: true`, `from` и `to` — префиксы с завершающим `/` (`docs/math/` → `archive/math/`). Все объекты переносятся **в одной транзакции**. При любом конфликте без `overwrite` → 409 с `details.conflicts` (до 100 путей), ничего не меняется.
- Нельзя переместить префикс внутрь самого себя (`a/` → `a/b/`) → 400.
- Проверки §4.2 для целевых путей.
- Ответ: `{"moved": 12}`.
- Ссылки продолжают работать (привязаны к object ID). **Публичные URL меняются.** Редиректы со старых путей в v1 не делаются (см. §18, §19).

#### `DELETE /api/v1/objects/{path...}` — удаление
Query: `recursive` (для префикса, путь с завершающим `/`), `purge` (bool, окончательно; требует `admin`).
- По умолчанию перенос в корзину. Ответ: `{"deleted": 3, "trash_ids": ["01j..."]}`.
- Нерекурсивное удаление пути, который является директорией → 400 с подсказкой про `recursive`.
- `If-Match` поддерживается для одиночного объекта.

#### Корзина
- `GET /api/v1/trash?prefix=&limit=&cursor=` → `{"entries":[{ "id", "original_path", "deleted_at", "purge_at", "object": {...Object} }], "next_cursor": ""}`
- `POST /api/v1/trash/{id}/restore` с телом `{"path": "<optional new path>", "overwrite": false}` → `200` Object. Восстанавливает тот же `id` объекта (ссылки снова работают). Конфликт пути → 409.
- `DELETE /api/v1/trash/{id}` → окончательное удаление (`admin`). `204`.
- `DELETE /api/v1/trash?older_than=7d` → очистка (`admin`). `{"purged": n}`. Без `older_than` чистит всё.

#### Ссылки
- `POST /api/v1/links` с `{"path": "...", "ttl": "2h", "note": "for the reviewer"}` (вместо `ttl` можно `expires_at`) → `201`:
  ```json
  { "id": "01j...", "object_id": "01j...", "path": "docs/x.pdf",
    "url": "https://store.example.com/s/01j.../x.pdf?exp=1760100000&sig=...",
    "created_at": "...", "expires_at": "...", "revoked_at": null, "note": "for the reviewer" }
  ```
- `GET /api/v1/links?path=&include_expired=false&include_revoked=false` → `{"links":[...]}`. `url` возвращается и в листинге (сервер может его пересобрать, т.к. хранит `expires_at`, а подпись детерминирована).
- `DELETE /api/v1/links/{id}` → отзыв, `204`.

---

## 11. Публичная часть (не API)

### 11.1 Маршруты

| Маршрут | Обработка |
|---|---|
| `GET /` | индекс корня |
| `GET /{dir}/` (завершающий `/`) | индекс директории (только public-потомки); нет public-потомков → 404 |
| `GET /{path}` | объект public/unlisted → отдача; private, не существует, просрочен, в корзине → 404; если это директория с public-потомками → `301` на `/{path}/` |
| `HEAD` для всего выше | то же без тела |
| `GET /s/{linkID}/{filename}` | подписанная ссылка (§8.2) |
| `GET /_vela/assets/*` | статика темы индекса (CSS, шрифты, иконки), embed или из `VELA_THEME_DIR` |
| `GET /robots.txt` | генерируется сервером по `VELA_ROBOTS`: `deny` (по умолчанию) → `Disallow: /`; `allow` → `Allow: /`, `Disallow: /s/`, `Disallow: /api/` (решение по §19.9) |
| `GET /favicon.ico` | из темы |
| `GET /healthz` | §10.4 |
| остальные методы на публичных маршрутах | `405` |

### 11.2 HTML-индекс

- Рендер через `html/template` (автоэкранирование), тема встроена через `embed`.
- **Кастомизация**: если задан `VELA_THEME_DIR`, файлы оттуда (`layout.html`, `index.html`, `error.html`, `assets/...`) переопределяют встроенные по имени. Шаблоны парсятся при старте; ошибка в кастомной теме → сервер не стартует с понятной ошибкой (fail fast).
- Данные для шаблона (документировать в `docs/theme.md` как «контракт темы»):
  - `Site` (title из `VELA_SITE_TITLE`, public URL), `Path`, `Breadcrumbs` (имя + URL), `Dirs` (имя, URL, количество public-объектов внутри, дата последнего изменения), `Files` (имя, URL, title, description, tags, size, human size, content type, updated_at), `Parent` URL, `GeneratedAt`, `Version`.
- Сортировка: директории сначала, затем файлы, по имени (natural sort SHOULD).
- Отображаются только `public`. Просроченные (`expires_at < now`) не отображаются.
- `?format=json` на индексе MAY возвращать те же данные в JSON (удобно для скриптов, без раскрытия non-public).
- Дизайн дефолтной темы: аккуратный, адаптивный (телефон), светлая/тёмная тема через `prefers-color-scheme`, без JS-зависимостей. Иконки по типу файла (inline SVG).
- Заголовки: `Cache-Control: public, max-age=60`, `Content-Security-Policy` строгий для страниц индекса (`default-src 'self'; style-src 'self'; img-src 'self' data:`).
- Страницы ошибок (404, 410 для истёкших ссылок) рендерятся тем же `error.html`.

### 11.3 Отдача содержимого (общий код для публичного URL, API-скачивания и подписанных ссылок)

Функция вида `deliver(w, r, obj, policy)`, где `policy` задаёт кэш/robots/referrer-заголовки.

- **Accel-режим** (`VELA_ACCEL_PREFIX="/_blobs/"`): ответ `200` с пустым телом и заголовками
  - `X-Accel-Redirect: /_blobs/<shard>/<blobID>`
  - `Content-Type`, `Content-Disposition` (передаются nginx-ом из ответа upstream)
  - служебные `X-Vela-Cache-Control`, `X-Vela-Robots`, `X-Vela-Referrer-Policy`, которые nginx выставляет через `$upstream_http_x_vela_*` в internal location (§14). Причина: при internal redirect nginx не пробрасывает произвольные заголовки upstream, а переменные `$upstream_http_*` остаются доступными.
  - **MUST**: интеграционный тест (или задокументированная ручная проверка в `CONTRIBUTING.md`) с реальным nginx, что `Content-Type`, `Content-Disposition`, `Cache-Control`, `X-Robots-Tag` действительно доходят до клиента. Если какой-то заголовок не доходит, переносить его через `X-Vela-*` + `add_header`.
- **Direct-режим** (`VELA_ACCEL_PREFIX=""`): `http.ServeContent` с `modtime = updated_at`, `ETag: "sha256:<hex>"`, те же заголовки, что выше, но выставленные напрямую.
- Политики:
  - public: `Cache-Control: public, max-age=300` (контент по тому же пути может быть перезаписан, поэтому не immutable);
  - unlisted: то же + `X-Robots-Tag: noindex, nofollow`;
  - signed link и API-скачивание: `Cache-Control: private, no-store`, `X-Robots-Tag: noindex, nofollow`, `Referrer-Policy: no-referrer`.
- Всегда: `X-Content-Type-Options: nosniff`.
- HTML/SVG, загруженные пользователем, отдаются с их типом (чтобы страницы работали как страницы). Это безопасно в нашей модели: у origin нет cookies и браузерной сессии, API работает только по заголовку `Authorization`, CSRF невозможен. Альтернатива с `Content-Security-Policy: sandbox` — см. §19.

---

## 12. CLI `vela`

### 12.1 Установка

- `go install go.xchunk.org/vela/cmd/vela@latest` (требует vanity-мету на `go.xchunk.org/vela`, §15.4).
- `make install` — то же из локального клона, с вшитыми версией/коммитом/датой через ldflags.
- Версия при `go install ...@vX.Y.Z` берётся из `debug.ReadBuildInfo()` (Main.Version), если ldflags не заданы.

### 12.2 Конфиг

- Путь: `$XDG_CONFIG_HOME/vela/config.toml` (через `os.UserConfigDir()`), переопределяется `--config` или `VELA_CONFIG`.
- Формат TOML, профили:

```toml
default_profile = "main"

[profiles.main]
url = "https://store.example.com"
key = "vela_EXAMPLEKEY0_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
default_visibility = "private"   # optional, applies to put/new when no flag given

[profiles.docs]
url = "https://store.example.com"
key = "vela_..."
```

- Приоритет: флаг (`--url`, `--profile`) > env (`VELA_URL`, `VELA_KEY`, `VELA_PROFILE`) > конфиг.
- **MUST**: если у файла конфига права шире `0600` (на Unix), CLI отказывается работать с ошибкой и подсказкой `chmod 600 <path>` (как ssh). На Windows проверка пропускается.
- `vela login` создаёт/обновляет профиль: спрашивает URL (без встроенного значения по умолчанию; существующий URL профиля сохраняется) и ключ (ввод без эха, `golang.org/x/term`), проверяет его через `/api/v1/info`, пишет файл атомарно (tmp + rename) с правами 0600, директорию с 0700.
- Ключ никогда не печатается целиком (в `vela whoami`/`--verbose` маскируется: `vela_k3f9abcd12_****`).

### 12.3 Команды

Общие флаги: `--profile`, `--url`, `--config`, `--json` (машинный вывод, стабильный формат = DTO из `api/`), `-q/--quiet`, `-v/--verbose` (HTTP-трассировка без секретов), `--no-color` (+ уважать `NO_COLOR`), `--timeout`.

Удалённые пути пишутся без схемы: `docs/math/q1.pdf`, ведущий `/` допустим. Директория обозначается завершающим `/`.

| Команда | Описание |
|---|---|
| `vela login [--profile name]` | §12.2 |
| `vela logout [--profile name]` | удалить профиль |
| `vela whoami` | имя ключа, scope, префикс, URL сервера |
| `vela version` | версия CLI, версия и api_version сервера, предупреждение при несовместимости |
| `vela ls [prefix] [-r] [-l] [--visibility v]` | листинг. `-l`: длинный формат (visibility, size, updated, title). Видимость подсвечивается цветом/маркером. Без аргумента — корень |
| `vela stat <path>` | все метаданные объекта + публичный URL |
| `vela put <local>... [remote] [flags]` | загрузка, §12.4 |
| `vela new <remote> [--content s \| --stdin \| --edit] [flags]` | создание из сырого контента: строка, stdin или открыть `$EDITOR` (`$VISUAL`, fallback `vi`) с временным файлом; пустой результат редактора → отмена |
| `vela get <remote> [local] [-r] [--force]` | скачать; `-` как local → stdout; без `--force` не перезаписывает локальные файлы |
| `vela cat <remote>` | вывести в stdout |
| `vela mv <from> <to> [--force]` | переименовать/переместить. Если `from` заканчивается на `/` → рекурсивно по префиксу. Если `to` заканчивается на `/` и `from` — файл, то перемещение внутрь директории с тем же именем |
| `vela rm <path>... [-r] [--purge] [-y]` | удаление в корзину. Для `-r` и `--purge` — подтверждение (`y/N`), отключается `-y` или неинтерактивным stdin + `-y` (без `-y` в неинтерактивном режиме → ошибка, а не молчаливое удаление) |
| `vela trash ls [prefix]` | содержимое корзины с датой автоудаления |
| `vela trash restore <id\|original_path> [--to path] [--force]` | восстановление; по пути восстанавливается самая свежая запись |
| `vela trash purge [<id>...] [--older-than 7d] [-y]` | окончательное удаление |
| `vela meta <path>` | показать метаданные |
| `vela meta <path> --title t --description d --tag x --untag y --content-type ct --disposition attachment --expires 7d\|<RFC3339>\|none` | изменить метаданные (PATCH с If-Match текущей ревизии) |
| `vela vis <path>... <public\|unlisted\|private> [-r]` | быстрая смена видимости (в т.ч. для всего префикса) |
| `vela share <path> [--ttl 2h] [--note s] [--copy]` | создать подписанную ссылку и напечатать URL. `--copy` копирует в буфер обмена (`wl-copy` / `xclip` / `pbcopy` / `clip.exe`, если доступны; иначе предупреждение) |
| `vela links ls [path] [--all]` | список ссылок (по умолчанию только активные) с оставшимся временем |
| `vela links revoke <id>...` | отзыв |
| `vela url <path>` | напечатать публичный URL (для private — ошибка с подсказкой про `share`) |
| `vela open <path>` | открыть публичный URL в браузере (`xdg-open`/`open`/`start`) |
| `vela completion bash\|zsh\|fish` | автодополнение; **удалённые пути дополняются динамически** через `ls` (с таймаутом ~1.5s, при ошибке — без подсказок, без шума в терминале) |

### 12.4 `vela put` — подробно

- `vela put file.pdf` → `file.pdf` в корень.
- `vela put file.pdf docs/math/` → `docs/math/file.pdf`.
- `vela put file.pdf docs/math/q1.pdf` → ровно этот путь.
- `vela put a.pdf b.pdf docs/` → несколько файлов в директорию (последний аргумент обязан заканчиваться на `/`).
- `vela put -r ./dir docs/dir/` → рекурсивно, сохраняя структуру; симлинки не следуются (по умолчанию пропускаются с предупреждением), скрытые файлы (`.*`) пропускаются, если нет `--hidden`.
- `vela put - notes/x.txt` → из stdin.
- Флаги: `--public | --unlisted | --private` (взаимоисключающие), `--title`, `--description`, `--tag` (повторяемый), `--content-type`, `--disposition`, `--expires 7d|<RFC3339>`, `--force` (перезапись; без него upload идёт с `If-None-Match: *`, и существующий файл → понятная ошибка «already exists, use --force»), `-j N` (параллелизм для нескольких файлов, default 4), `--dry-run` (показать, что будет загружено и куда).
- CLI считает SHA-256 до или во время отправки и шлёт `X-Vela-SHA256`. Для потоковой отправки без двойного чтения: если источник — обычный файл, хэш считается первым проходом (файл можно перечитать); для stdin — без заголовка.
- Прогресс-бар для файлов > 1 MiB в интерактивном терминале (stderr), отключается `-q` и `--json`.
- В конце печатается публичный URL (для public/unlisted) или подсказка `vela share` (для private). С `--json` — массив Object.
- Ретраи: сетевые ошибки и 5xx/429 — до 3 попыток с экспоненциальной задержкой, только для идемпотентных операций (GET, PUT с `If-None-Match`/`If-Match`, DELETE). POST create и move не ретраятся автоматически.

### 12.5 Коды выхода

| Код | Значение |
|---|---|
| 0 | успех |
| 1 | прочая ошибка |
| 2 | неверное использование (флаги/аргументы) |
| 3 | not found |
| 4 | conflict / already exists / precondition failed |
| 5 | auth (401/403) |
| 6 | сеть/недоступность сервера |
| 7 | частичный успех (часть файлов в пакетной операции упала) |

### 12.6 UX-требования

- Ошибки в stderr, короткие и с подсказкой действия (`already exists: docs/a.pdf (use --force to overwrite)`).
- С `--json` stdout содержит только JSON (ошибки — JSON-объектом `{"error":{...}}` в stdout и ненулевой код выхода), чтобы CLI был удобен для скриптов и для ci.
- Таблицы выравниваются, размеры human-readable (KiB/MiB), время в локальной зоне.
- Цвета только для TTY.

---

## 13. Конфигурация сервера `velad`

Конфиг через переменные окружения (удобно для `docker run --env-file`), каждая продублирована флагом `velad serve --...`.

| Переменная | Default | Описание |
|---|---|---|
| `VELA_LISTEN` | `:8080` | адрес HTTP |
| `VELA_DATA_DIR` | `/data` | корень данных |
| `VELA_PUBLIC_URL` | — (**обязательна**) | `https://store.example.com`, без `/` в конце |
| `VELA_ACCEL_PREFIX` | empty (direct) | пусто → direct-режим |
| `VELA_TRUSTED_PROXIES` | `127.0.0.1/32,::1/128` | CIDR, от которых доверяем `X-Real-IP`/`X-Forwarded-*` (§19.14) |
| `VELA_MAX_UPLOAD` | `2GiB` | лимит загрузки (согласовать с `client_max_body_size` nginx) |
| `VELA_MAX_CREATE` | `1MiB` | лимит `POST /objects` |
| `VELA_LINK_DEFAULT_TTL` | `24h` | |
| `VELA_LINK_MAX_TTL` | `30d` | |
| `VELA_LINK_RETENTION` | `7d` | сколько хранить записи истёкших ссылок |
| `VELA_TRASH_RETENTION` | `14d` | |
| `VELA_JANITOR_INTERVAL` | `24h` | |
| `VELA_SITE_TITLE` | `Vela` | заголовок индекса |
| `VELA_THEME_DIR` | пусто | кастомная тема |
| `VELA_LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `VELA_LOG_FORMAT` | `json` | `json`/`text` |
| `VELA_DEFAULT_VISIBILITY` | `unlisted` | видимость, если клиент не указал (§19.5) |
| `VELA_ROBOTS` | `deny` | `deny` → `robots.txt` запрещает всё; `allow` → разрешает индекс (§19.9) |

Подкоманды `velad`:

```
velad serve                    # default command (no args = serve)
velad key create|list|revoke
velad signing rotate [--revoke-all]
velad janitor run
velad backup <dest-dir>        # consistent snapshot: VACUUM INTO + hard-link/copy blobs
velad healthcheck              # used by Docker HEALTHCHECK (distroless has no curl)
velad version
velad migrate                  # run migrations explicitly (serve also runs them on start)
```

Конфиг валидируется при старте (fail fast с понятными ошибками): обязательный `VELA_PUBLIC_URL`, существование и права `VELA_DATA_DIR`, `LINK_DEFAULT_TTL ≤ LINK_MAX_TTL` и т.д.

Прочее:
- Логи: `log/slog`, одна строка на запрос (method, route pattern, status, bytes, duration, request_id, key_id; **никогда** не логировать ключи, подписи, полные query подписанных ссылок).
- Graceful shutdown по SIGTERM/SIGINT: перестать принимать, дождаться активных запросов (таймаут 30s), остановить janitor, закрыть БД.
- HTTP-таймауты: `ReadHeaderTimeout` 10s; для загрузок `ReadTimeout` не ставить глобально (большие файлы), вместо этого idle-таймаут на чтение тела через `http.ResponseController` deadlines.
- Panic recovery middleware → 500 + лог со стеком.

### 13.1 Схема БД (ориентир, финальная — на усмотрение реализации)

```sql
CREATE TABLE objects (
  id            TEXT PRIMARY KEY,           -- ULID
  path          TEXT NOT NULL UNIQUE,       -- normalized path
  blob_id       TEXT NOT NULL,
  size          INTEGER NOT NULL,
  sha256        TEXT NOT NULL,
  content_type  TEXT NOT NULL,
  disposition   TEXT NOT NULL DEFAULT 'inline',
  visibility    TEXT NOT NULL CHECK (visibility IN ('public','unlisted','private')),
  title         TEXT NOT NULL DEFAULT '',
  description   TEXT NOT NULL DEFAULT '',
  tags          TEXT NOT NULL DEFAULT '[]', -- JSON array
  created_at    INTEGER NOT NULL,           -- unix ms
  updated_at    INTEGER NOT NULL,
  expires_at    INTEGER,
  revision      INTEGER NOT NULL DEFAULT 1
);
-- prefix queries use range scans: path >= ? AND path < ? (prefix + 0xFF)

CREATE TABLE trash (
  id             TEXT PRIMARY KEY,          -- trash entry ULID
  object_id      TEXT NOT NULL,             -- original object id, reused on restore
  original_path  TEXT NOT NULL,
  snapshot       TEXT NOT NULL,             -- JSON of the object row
  deleted_at     INTEGER NOT NULL,
  deleted_by_key TEXT
);

CREATE TABLE links (
  id              TEXT PRIMARY KEY,
  object_id       TEXT NOT NULL,            -- no FK to objects: object may be in trash
  signing_key_id  TEXT NOT NULL,
  filename        TEXT NOT NULL,
  note            TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  expires_at      INTEGER NOT NULL,
  revoked_at      INTEGER,
  created_by_key  TEXT
);

CREATE TABLE signing_keys (id TEXT PRIMARY KEY, secret BLOB NOT NULL, created_at INTEGER NOT NULL, retired_at INTEGER);

CREATE TABLE api_keys (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, secret_hash BLOB NOT NULL,
  scope TEXT NOT NULL, prefix TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, last_used_at INTEGER, revoked_at INTEGER
);

CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL); -- schema version, janitor lock, etc.
```

- Драйвер: `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0`).
- PRAGMA: `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`, `synchronous=NORMAL`.
- Миграции — embed SQL-файлы с номерами, применяются в транзакции при старте; версия в `meta`.
- Один пул на запись (`SetMaxOpenConns(1)` для writer) и отдельный на чтение — или единый пул с аккуратными транзакциями; выбрать и задокументировать.

---

## 14. Reverse proxy examples

`examples/nginx/` contains three starting points: `fs-direct.conf`, `fs-accel.conf`, and `r2.conf`. Include exactly one in nginx's `http` context and install the shared snippets as described in `examples/README.md`. These are examples, not supported deployments; the operator owns TLS, firewall rules, reverse proxy security, backups, and updates.

All examples stream API request bodies, disable response buffering, use query-free access logs in both HTTP and HTTPS servers, and disable signed-link error logs. Security headers must appear exactly once even after an internal redirect. Upload limits must match `VELA_MAX_UPLOAD`; metadata headers require `large_client_header_buffers 4 64k`.

Go streams filesystem contents by default (`VELA_ACCEL_PREFIX=`). Opting into `VELA_ACCEL_PREFIX=/_blobs/` requires the matching internal locations in `accel-locations.conf`, including the signed-link location. Only Go decides whether an object may be served. ETag, cache, robots, and referrer policies must survive accel delivery without exposing internal headers. `make test-e2e` exercises the actual example through nginx.

## 15. Сборка, Docker, CI/CD

### 15.1 Makefile

Цели (минимум):

```
make build          # both binaries into ./bin with version ldflags
make build-cli
make build-server
make install        # install vela into $(go env GOBIN) or GOPATH/bin, with ldflags
make test           # go test -race ./...
make lint           # golangci-lint run
make fmt            # gofmt + goimports
make docker         # local docker build with VERSION/COMMIT/DATE build args
make dev            # run velad locally in direct mode with ./.dev-data and a printed admin key
make clean
```

Ldflags: `-s -w -X go.xchunk.org/vela/internal/version.Version=$(VERSION) -X ...Commit=$(COMMIT) -X ...Date=$(DATE)`, `VERSION` из `git describe --tags --always --dirty`. Сборка с `-trimpath`, `CGO_ENABLED=0`.

### 15.2 Dockerfile

```dockerfile
# syntax=docker/dockerfile:1
FROM golang:<latest-stable>-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev COMMIT=none DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X go.xchunk.org/vela/internal/version.Version=${VERSION} \
                -X go.xchunk.org/vela/internal/version.Commit=${COMMIT} \
                -X go.xchunk.org/vela/internal/version.Date=${DATE}" \
      -o /out/velad ./cmd/velad

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/velad /velad
USER 65532:65532
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD ["/velad", "healthcheck"]
ENTRYPOINT ["/velad"]
CMD ["serve"]
```

- Версия Go — актуальная стабильная на момент реализации (проверить на go.dev), одна и та же в `go.mod` (`go` + `toolchain`), Dockerfile и CI.
- Labels OCI (`org.opencontainers.image.source`, `version`, `revision`) — через build-args.
- Архитектура образа — `linux/amd64` (§19.3).
- Бинарник кладётся в `/usr/local/bin/velad` (а не `/velad`), чтобы `docker exec vela velad ...` работал: в distroless `/` не входит в `PATH` (см. §21).

### 15.3 GitHub Actions

**`ci.yml`** — на push в любую ветку и на PR:
1. `actions/setup-go` (версия из `go.mod`), кеш модулей.
2. `go vet ./...`, `golangci-lint` (официальный action), `go test -race ./...`.
3. `go build ./cmd/...`.
4. `docker build` без push (проверка, что образ собирается).

**`release.yml`** — на push тега `v*`:
1. Те же тесты.
2. `docker/setup-buildx-action`, `docker/login-action` в Docker Hub с `username: ${{ vars.DOCKERHUB_USERNAME }}` (repository variable) и `password: ${{ secrets.DOCKERHUB_TOKEN }}` (Docker Hub PAT с правами Read & Write).
3. `docker/metadata-action` → теги `X.Y.Z`, `X.Y`, `latest`.
4. `docker/build-push-action` с build-args `VERSION`, `COMMIT`, `DATE`, push в `docker.io/xchunk/vela`, платформы `linux/amd64,linux/arm64`. Образ **публичный**.
5. GitHub Release с собранными бинарниками `vela` под linux/darwin/windows × amd64/arm64 + `checksums.txt` (решение по §19.10).

Секреты не выводятся в логи, permissions у workflow минимальные (`contents: read`; `contents: write` только у job-а, создающего GitHub Release).

### 15.4 Vanity import

`go.xchunk.org/vela` должен отдавать:

```html
<meta name="go-import" content="go.xchunk.org/vela git https://github.com/x-chunk/vela">
<meta name="go-source" content="go.xchunk.org/vela https://github.com/x-chunk/vela https://github.com/x-chunk/vela/tree/master{/dir} https://github.com/x-chunk/vela/blob/master{/dir}/{file}#L{line}">
```

для запросов `?go-get=1` к `go.xchunk.org/vela` **и** ко всем подпутям (`/vela/cmd/vela`). Это настраивается в существующей vanity-инфраструктуре на сервере vanity-модуля, вне этого репо; требования MUST описать в `CONTRIBUTING.md`. Репозиторий публичный (§19.1), основная ветка — `master`.

---

## 16. Деплой (точка входа в README, примеры в examples/)

1. На сервере: `sudo mkdir -p /srv/vela && sudo chown 65532:65532 /srv/vela && sudo chmod 755 /srv/vela`.
2. `/etc/vela/vela.env` (права 600) по образцу `examples/env/fs.env.example`, минимум `VELA_PUBLIC_URL=https://store.example.com`.
3. (Логин в реестр не нужен: образ публичный.)
4. Запуск:
   ```
   docker run -d --name vela --restart unless-stopped \
     -p 127.0.0.1:8080:8080 \
     -v /srv/vela:/data \
     --env-file /etc/vela/vela.env \
     xchunk/vela:1.0.0
   ```
5. DNS A/AAAA для `store.example.com`, nginx-конфиг из `examples/nginx/`, certbot, `nginx -t && systemctl reload nginx`.
6. `docker exec vela velad key create --name workstation --scope admin` → ключ в `vela login` на компьютере.
7. Проверка: `vela whoami`, `vela new hello.txt --content hi --public`, открыть `https://store.example.com/`.

Обновление: `docker pull xchunk/vela:X.Y.Z && docker stop vela && docker rm vela && docker run ...` (та же команда с новым тегом). Миграции БД применяются автоматически при старте; перед обновлением — `velad backup`.

Бэкап: `docker exec vela velad backup /data/backups/<date>` или остановка контейнера и копирование `/srv/vela` целиком. Всё состояние — одна папка.

---

## 17. Качество кода, тесты, документация

### 17.1 Код

- Комментарии **на американском английском, в production-стиле**: doc-комментарий у каждого экспортируемого идентификатора и пакета (`doc.go`), комментарии объясняют **почему**, а не пересказывают код. Инварианты безопасности (constant-time compare, почему 404 вместо 403, почему tmp на той же FS, почему не логируем query) прокомментированы в месте реализации.
- Идентификаторы, логи, сообщения ошибок API и CLI — на английском.
- `gofmt`, `goimports`, `golangci-lint` с разумным набором (errcheck, govet, staticcheck, gosec, revive, ineffassign, unused, bodyclose, noctx, errorlint).
- Ошибки оборачиваются с контекстом (`fmt.Errorf("...: %w", err)`), сравнение через `errors.Is/As`.
- `context.Context` пробрасывается везде, где есть I/O.
- Минимум зависимостей. Ожидаемые: `spf13/cobra`, `modernc.org/sqlite`, `oklog/ulid/v2` (или собственная реализация), `golang.org/x/term`, `golang.org/x/text`, TOML-библиотека (`pelletier/go-toml/v2` или `BurntSushi/toml`). Прогресс-бар и clipboard — по возможности без внешних зависимостей. Новые зависимости обосновывать в PR/AGENTS.md.

### 17.2 Тесты (MUST)

- Unit: валидация и нормализация путей (таблицы с краевыми случаями, включая Unicode), парсинг длительностей, формат ключей и хэширование, подпись/проверка ссылок (истечение, подмена `exp`, подмена `linkID`, отозванные, ротация ключа), видимость и политика заголовков, merge patch, конфликты файл/директория.
- Сервис/хранилище: на временной директории и временной SQLite. Атомарность загрузки (обрыв посреди потока, превышение лимита, mismatch хэша), перезапись с сохранением метаданных, рекурсивный move в одной транзакции, корзина и restore с тем же object ID, janitor на фиктивных часах.
- HTTP: `httptest` для всех эндпоинтов, включая коды ошибок и scope/префикс ключей, публичные маршруты (public/unlisted/private, 404 vs 301 для директорий, индекс без non-public).
- `client/` тестируется против in-process `velad` (`internal/testutil`).
- CLI: тесты ключевых команд против in-process сервера (резолв путей `put`, `--json` вывод, коды выхода, проверка прав конфига).
- MAY: e2e-тест с nginx в CI (контейнер nginx + velad), проверяющий accel-заголовки (§11.3). Если не делается — ручной чек-лист в `CONTRIBUTING.md`.

### 17.3 Документация

- `README.md` и техническая документация (этап 4): §18, этап 4.
- `AGENTS.md` (этап 1): §18, этап 1.
- `docs/SPEC.md`: этот документ, обновляемый, если в ходе реализации что-то осознанно изменилось (с пометкой, что и почему).

---

## 18. Процесс разработки (4 этапа)

### Этап 1 — реализация (Claude Code)

- Задать уточняющие вопросы (§19), затем реализовать всё из этой спеки: сервер, CLI, общий код, тесты, Makefile, Dockerfile, workflows, референсный nginx-конфиг, `examples/env/fs.env.example`.
- Создать **`AGENTS.md`** (не `CLAUDE.md`: файл должен читаться ревью-агентом Astra). Содержимое:
  - назначение проекта в 3–5 предложениях и ссылка на `docs/SPEC.md`;
  - карта репозитория (что в каком пакете, правила зависимостей между слоями: `server → service → (store, blob, sign)`, `api`/`client` не импортируют `internal/server|service|store`);
  - команды: build, test, lint, dev-запуск;
  - конвенции: стиль комментариев (американский английский, production), обработка ошибок, логирование (что нельзя логировать), именование;
  - **инварианты безопасности для ревью** (чек-лист): constant-time сравнения; private → 404; ключи только в `Authorization`; ключи и подписи не логируются; атомарность записи; проверка префикса ключа на **всех** операциях, включая move-цель и restore; `exp` в URL сверяется с БД; лимиты размера; права файлов; доверие `X-Forwarded-*` только от trusted proxies;
  - что считается breaking change в API v1;
  - чего не делать (не добавлять compose, не вводить cgo, не логировать query подписанных ссылок и т.п.).
- В конце этапа: всё собирается, `make test` и `make lint` зелёные, `make dev` поднимает рабочий сервер.

### Этап 2 — ревью (Astra)

Astra читает `AGENTS.md` и `docs/SPEC.md`, проводит ревью на соответствие спеке, безопасность, корректность, архитектуру и качество комментариев. Результат — список замечаний (формат определяется Astra).

### Этап 3 — внесение правок по ревью

Исправления по замечаниям этапа 2. Осознанно отклонённые замечания фиксируются с обоснованием (в `AGENTS.md` или в описании изменений). Тесты и линтер снова зелёные.

### Этап 4 — документация

Уточнённый объём этапа: документация на английском, с разделением ответственности.

1. `README.md`: краткое описание, модель видимости (unlisted не защищает доступ), один рабочий Getting Started, установка CLI и ссылки на примеры деплоя.
2. Основная пользовательская документация: `https://docs.xchunk.org/vela`. Полный User Manual и создание сайта выполняются отдельно, вне этого этапа.
3. `docs/README.md`: навигация и правила сопровождения документации внутри репозитория.
4. Технические справки в `docs/`: архитектура и API-контракты со ссылками на код, конфигурация и её приоритеты, хранение и доставка, гарантии backup/migration, контракт темы.
5. `CONTRIBUTING.md`: требования к разработке, границы тестовых наборов и сопровождение распространения. Предпосылки nginx/Docker и сценарии интеграции остаются рядом с файлами в `examples/`.

Не дублировать полный User Manual в репозитории и не добавлять инструменты сборки сайта. Менять техническую справку вместе с поведением, а намеренные отклонения от спеки фиксировать в §21.

### После v1 (вне скоупа, для понимания направления)

- Import existing files with `vela put -r`; operators can configure redirects from old public URLs.
- Перевод ci с SSH на `go.xchunk.org/vela/client` или `vela --json` с ключом `rw` + префикс `docs/`.
- Возможно: редиректы при переименовании (alias-таблица), бессрочные «публичные токены», веб-UI.

---

## 19. Открытые вопросы (задать до начала реализации)

> **Статус: все вопросы закрыты 2026-10-10** перед этапом 1. Ответы — под каждым вопросом; влияние на остальной текст отражено по месту и в §21.

1. **Repository and image:** public GitHub repository and Docker Hub image; anonymous pulls are supported.
2. **Project identity:** module `go.xchunk.org/vela`, source `https://github.com/x-chunk/vela`, official image `xchunk/vela`.
3. **Image platforms:** `linux/amd64` and `linux/arm64`.
4. **Release credentials:** `secrets.DOCKERHUB_USERNAME` and `secrets.DOCKERHUB_TOKEN`; image name in `vars.DOCKERHUB_IMAGE`. The token is scoped to the image repository with Read & Write access.
5. **Видимость по умолчанию**: `private` (безопасно) или `unlisted`/`public` (удобнее для «свалки»)?
   - **Ответ:** `unlisted`.
6. **Просроченные объекты** (`expires_at`): скачивать через API до ежедневной чистки можно (как в спеке) или они недоступны совсем?
   - **Ответ:** через API скачивать можно до ежедневной чистки; публичный URL и подписанные ссылки — 404 сразу.
7. **Сроки**: `VELA_TRASH_RETENTION` 14d, `VELA_LINK_MAX_TTL` 30d, `VELA_MAX_UPLOAD` 2 GiB — подходят?
   - **Ответ:** подходят, оставляем как есть.
8. **Пользовательские HTML/SVG**: отдавать как есть (страницы работают) или с `Content-Security-Policy: sandbox` (безопаснее, но JS на загруженных страницах не работает)?
   - **Ответ:** отдавать как есть (без `sandbox`).
9. **robots.txt**: разрешать поисковикам индексировать публичный индекс или закрыть всё (`Disallow: /`)?
   - **Ответ:** настраивается через `VELA_ROBOTS=allow|deny`, по умолчанию `deny` (`Disallow: /`).
10. **GitHub Release с бинарниками CLI**: нужен, или достаточно `go install` / `make install`?
   - **Ответ:** нужен.
11. **Индекс директории**: если в директории есть public-объект `index.html`, отдавать его вместо автоиндекса (мини-хостинг статических сайтов) или всегда автоиндекс?
   - **Ответ:** всегда автоиндекс; `index.html` — обычный файл.
12. **Язык README**: английский (как комментарии) или русский?
   - **Ответ:** английский.
13. **Как именно вызывается Astra** на этапах 2–3 (кто вносит правки по ревью — Astra сама или Claude Code по её отчёту)? Нужен ли определённый формат отчёта ревью в репо (например, `REVIEW.md`)?
   - **Ответ:** Astra пишет `REVIEW.md` в корне репозитория с нумерованными замечаниями; правки вносит Claude Code и отмечает статус каждого замечания там же (отклонённые — с обоснованием).
14. **Trusted proxies:** loopback only by default. For Docker bridge networking, inspect the actual gateway and explicitly trust only the proxy address (the common `172.17.0.1` is an example). Linux host networking needs only loopback when the proxy runs on the same host.


---

## 20. Критерии готовности v1

- [ ] `make test`, `make lint` зелёные; CI зелёный на `master`; тег `v1.0.0` публикует публичный образ `xchunk/vela` в Docker Hub.
- [ ] Все эндпоинты §10 реализованы и покрыты тестами, включая ошибки и ограничения scope/префикса.
- [ ] Три уровня видимости работают по таблице §3; private по прямому URL — 404; индекс показывает только public.
- [ ] Подписанные ссылки: создаются, работают после переименования, истекают по `exp` (410), отзываются, не продлеваются подменой `exp`; ротация ключа подписи работает.
- [ ] Janitor выполняет все задачи §9 по расписанию и вручную.
- [ ] Загрузка потоковая, атомарная, с лимитом и проверкой хэша; перезапись сохраняет id, метаданные и ссылки.
- [ ] Рекурсивный move атомарен; конфликты файл/директория отлавливаются.
- [ ] CLI реализует все команды §12 с `--json`, кодами выхода, проверкой прав конфига, автодополнением удалённых путей.
- [ ] Контейнер: distroless, nonroot, healthcheck, работает с bind-mount `/srv/vela`, nginx читает блобы без ручной возни с правами.
- [ ] Accel-заголовки проверены через реальный nginx (тест или задокументированный чек-лист).
- [ ] `AGENTS.md` после этапа 1, `README.md` после этапа 4 соответствуют §18.

---

## 21. Изменения спецификации в ходе реализации

Осознанные отступления от исходного текста (дата, что и почему).

| Дата | Раздел | Изменение | Причина |
|---|---|---|---|
| 2026-10-10 | §0, §15.3, §16, §20 | Образ публикуется **публично** в Docker Hub `xchunk/vela`, с анонимным pull. | Решение владельца. |
| 2026-10-10 | §5.2 | Блобы шардируются по **последним** 4 символам ULID (`<id[22:24]>/<id[24:26]>/<id>`), а не по первым. | Первые символы ULID — метка времени: все блобы за ~3 суток попадали бы в одну директорию, шардирование не работало бы. Последние символы — случайная часть. |
| 2026-10-10 | §15.2 | Бинарник в образе — `/usr/local/bin/velad`. | В distroless `/` не входит в `PATH`, и команды `docker exec vela velad ...` из §7.5/§16 не работали бы. |
| 2026-10-10 | §11.3, §14 | В accel-режиме velad не шлёт `Cache-Control` напрямую, только `X-Vela-Cache-Control`. | nginx при `X-Accel-Redirect` сам пробрасывает upstream `Cache-Control`; вместе с `add_header` из `X-Vela-Cache-Control` получился бы дубль. |
| 2026-10-10 | §7.4 | `GET /api/v1/links` требует scope `rw`. | Листинг возвращает рабочие подписанные URL, то есть выдаёт доступ к объектам. |
| 2026-10-10 | §12.4 | Если повтор `PUT` с `If-None-Match: *` получил `409 already_exists`, клиент сверяет sha256 существующего объекта и при совпадении считает загрузку успешной. | Первый запрос мог дойти до сервера, а ответ — потеряться. |
| 2026-10-10 | §14 | `log_format` на уровне `http`; `location`-блоки в `examples/nginx/common.conf` (include). Добавлены `server_tokens off` и `proxy_set_header X-Request-ID $request_id` (+ `add_header X-Request-ID` во внутренней location). | В исходном конфиге `log_format` стоял внутри `server` — `nginx -t` его отвергает. Общий сниппет позволяет e2e-тесту проверять ровно разворачиваемый конфиг. Один request ID в логах nginx и velad. |
| 2026-10-10 | §11.3, §17.2 | Интеграционный тест с реальным nginx 1.30 (`test/e2e`, `make test-e2e`, отдельный job в CI) вместо ручного чек-листа. Подтверждено: `Content-Type` и `Content-Disposition` переживают `X-Accel-Redirect`, `Cache-Control`/`X-Robots-Tag`/`Referrer-Policy` приходят ровно один раз через `X-Vela-*`, внутренние заголовки не утекают, Range/HEAD/ETag/304 работают. | MUST из §11.3. |
| 2026-10-10 | §15.2 | Базовый образ `gcr.io/distroless/static-debian13:nonroot`; в образе создан `/data`, принадлежащий 65532. | Debian 13 — текущий стабильный выпуск distroless. `/data` с нужным владельцем позволяет запускать контейнер и с анонимным томом. |
| 2026-10-10 | §6, §5.4 | Добавлены пакеты `internal/domain` (сущности), `internal/wire` (сборка сервиса и адаптеры store/blob → интерфейсы сервиса), `internal/velad` (подкоманды сервера), `internal/mimetype`, `internal/clock`, `internal/units`, `internal/archtest`. | Интерфейсы объявлены в `service`, а Go требует точного совпадения сигнатур методов, поэтому нужен тонкий адаптер в одном месте. `mimetype` убирает из `service` импорт `net/http`. `archtest` машинно проверяет правила слоёв. |
| 2026-10-10 | §13.1 | Таблица `trash` хранит все колонки объекта (а не JSON `snapshot`); `trash.object_id` и `blob_id` уникальны. Имена API-ключей уникальны только среди неотозванных. Пулы: один writer (1 соединение, `BEGIN IMMEDIATE`) и reader-пул (`query_only`), оба WAL. | Колонки индексируются (поиск сиротских блобов, фильтр по пути); отозванный ключ освобождает имя; писатели сериализованы без `SQLITE_BUSY` на апгрейде блокировки. |
| 2026-10-10 | §10.3 | Добавлен код `method_not_allowed` (405) для API. | 405 на `/api/v1/*` должен иметь тот же JSON-формат ошибки. |
| 2026-10-10 | §10.1, §10.4 | Все запросы под `/api/` аутентифицируются до маршрутизации (неизвестный эндпоинт без ключа → 401). Неканонические пути (`a//b`, `.`/`..`) → 400 `invalid_path` вместо редиректа. JSON-тела разбираются строго: неизвестные поля → 400 `invalid_request`. | Не раскрывать набор эндпоинтов; не превращать молча `a//b` в `a/b` для PUT; опечатка в опции не должна становиться no-op. |
| 2026-10-10 | §7.4, §10.4 | Листинг каталога-предка префикса ключа (например, корня для ключа `docs/`) сужается до префикса, а не даёт 403; несвязанный каталог — 403. Записи корзины вне префикса ключа — 404. | «Листинг автоматически ограничивается префиксом»; ключ видит путь к своему поддереву и ничего больше. |
| 2026-10-10 | §10.4 | `GET /api/v1/trash` принимает также `path` (точный исходный путь) — для `vela trash restore <path>`. `DELETE /api/v1/objects/` (корень) с `recursive` отклоняется (400). `PUT` с `If-Match` на несуществующий объект → 412. | Удобство CLI; защита от удаления всего хранилища одной командой; семантика HTTP-предусловий. |
| 2026-10-10 | §8.3, §10.4 | В `Link` поле `path` пустое, если объект в корзине или удалён; `url` — `null`, если ключ подписи уничтожен (`--revoke-all`). Восстановление из корзины снимает уже истёкший `expires_at`. | Ссылка не работает, пока объект не «живой»; URL без ключа построить нельзя. Без снятия срока janitor сразу вернул бы объект в корзину. |
| 2026-10-10 | §12.4 | `vela put -r DIR REMOTE/` кладёт **содержимое** `DIR` под `REMOTE/` (как `cp -r DIR/. REMOTE/`). При частичном сбое с `--json` выводится один документ `{"error":{"code":"partial_failure","details":{"failed":[...],"objects":[...]}}}`, код выхода 7. | Однозначность примера `put -r ./dir docs/dir/`; в stdout всегда ровно один JSON-документ. |
| 2026-10-10 | §7.5 | `velad key create` печатает в stdout **только** ключ, остальное — в stderr. | `KEY=$(docker exec vela velad key create ...)` захватывает ровно ключ. |
| 2026-10-10 | §15.1, §15.3 | golangci-lint закреплён на v2.14.0 и запускается через `go run` (Makefile) и `install-mode: goinstall` (CI). Добавлены цели `make test-e2e` и `make dist`. | Бинарник линтера, собранный другим тулчейном, не читает export data Go 1.27.2. `make dist` — те же архивы CLI, что публикует релиз. |
| 2026-10-10 | §5.3, §9, §13.1 | Заменённое, очищенное (purge) и сиротское содержимое не удаляется сразу: транзакция, снимающая последнюю ссылку, ставит блоб в очередь `blob_gc` (миграция 0002); сборщик удаляет файл не раньше чем через 15 минут и только после `PRAGMA wal_checkpoint(FULL)`, повторно проверяя отсутствие ссылок. Сборщик работает в задаче janitor `blob_gc` и в `serve` каждые 5 минут. Ревью R-002, R-012. | При `synchronous=NORMAL` закоммиченная транзакция может откатиться после сбоя питания; немедленный unlink оставил бы строку без содержимого. Период ожидания сохраняет старое содержимое для запросов, разрешённых до перезаписи (в т.ч. для nginx). |
| 2026-10-10 | §9 | Межпроцессная блокировка janitor продлевается каждые lease/3 (lease по умолчанию 1 ч) всё время прохода; если владение потеряно (захват другим процессом или длительный сбой продления), проход прерывается. Ревью R-029. | Фиксированная аренда без продления истекала у длинного прохода, и второй процесс мог начать параллельную чистку. |
| 2026-10-10 | §13, §16 | `velad backup` держит межпроцессную блокировку `backup` (с продлением) от снимка БД до копирования последнего блоба; пока она взята, сборщик блобов ничего не удаляет. Отсутствующий или повреждённый (размер; с `--verify` — SHA-256) блоб делает backup неуспешным, частичные файлы удаляются. Параллельные backup исключены. Ревью R-001. | Перезапись или purge между снимком и копированием удаляли блоб, на который ссылается снимок; backup при этом завершался успехом. |
| 2026-10-10 | §11.3, §14 | Формат `vela_noquery` задан и в HTTP-сервере редиректа. Для `/s/` и новой внутренней location `/_blobs/s/` (velad отдаёт подписанные ссылки через `X-Accel-Redirect: <prefix>s/<shard>/<id>`) error log выключен. e2e-тест запускает весь `store.example.com.conf` с TLS и проверяет фактические журналы. Ревью R-007. | Error log nginx всегда пишет полную строку запроса с query (например, при недоступном upstream или отсутствующем файле); формат access log на него не влияет. |
| 2026-10-10 | §10.4, §11.3, §14 | Единственный валидатор содержимого — ETag `"sha256:<hex>"` в обоих режимах. В accel-режиме velad сам отвечает 304 на совпавший `If-None-Match` и передаёт ETag через `X-Vela-ETag`; внутренние location nginx: `etag off`, `if_modified_since off`, `add_header ETag`, удаление `Last-Modified`. `Last-Modified` не отправляется и в direct-режиме. Ревью R-013. | ETag nginx (mtime+size) и Last-Modified с секундной точностью совпадали у разного содержимого одного размера, перезаписанного в ту же секунду: клиент получал 304 или неверный Range. |
| 2026-10-10 | §10.4, §14 | nginx: `large_client_header_buffers 4 64k` в TLS-сервере; velad: `MaxHeaderBytes` 128 КиБ. Ревью R-014. | Метаданные PUT передаются percent-encoded в заголовках; допустимое описание из 4096 четырёхбайтовых символов — строка заголовка около 48 КиБ, а лимит nginx по умолчанию — 8 КиБ на строку. |
| 2026-10-10 | §13.1, §10.4 | Записи корзины получают монотонный `seq` (миграция 0003, таблица `counters`); листинг корзины, курсоры, восстановление по пути и очистка упорядочены по `seq`. Ревью R-030. | Случайная часть ULID не упорядочена внутри одной миллисекунды: восстановление по пути могло вернуть не последнюю удалённую версию. |
| 2026-10-10 | §12.2 | На Windows конфиг CLI сохраняется с защищённым DACL (только текущий пользователь и SYSTEM, без наследования), каталог, созданный для него, — так же; при загрузке конфиг, DACL которого даёт чтение кому-то ещё (или отсутствует), отвергается с подсказкой `icacls`. В CI добавлен job на `windows-latest`. Ревью R-010. | Биты 0600/0700 на Windows не задают права доступа (os.Chmod меняет только read-only), а проверка при загрузке пропускалась. |
| 2026-10-11 | §5.3; SPEC-storage §3–4 | `internal/content` defines the streaming blob contract; implementations stay independent of services. Filesystem publication uses an exclusive hard link from tmp followed by fsync and unlink of the temporary name. | Atomic publication must reject reuse of an immutable ID instead of replacing its previous content. Both directories are checked to share a filesystem. |
| 2026-10-11 | SPEC-storage §5.4 | SHA-256 ETag remains the sole validator; HEAD and conditional requests use SQLite without opening a blob. Last-Modified and If-Modified-Since remain disabled. | Preserves the R-013 fix: second-resolution timestamps can falsely validate content replaced within the same second. |
| 2026-10-11 | SPEC-storage §5.3, §6 | Multipart payload buffers include the separate first part and pool: `partSize × (concurrency + 2) × maxConcurrentUploads` (defaults: 128 MiB). Reserve at least `(concurrency + 4)` parts per active upload (defaults: 192 MiB) for capacity planning, including first-part growth, plus GC and process overhead; this is not an RSS bound. Corrected by R-005. Backend credentials/settings are environment-only. Part size is capped at 64 MiB and part concurrency at 16. | The pinned SDK uses a growing first-part allocation outside the pool as well as a read-ahead buffer; both must be included in memory planning. Credentials must not appear in command arguments. |
| 2026-10-11 | SPEC-storage §3, §5.5 | R2 health uses HeadBucket with a 45-second cache; write permissions are checked by smoke upload, not by the health probe. Delivery capabilities are transport-free methods adapted through the service; nginx accel stays in HTTP transport. | HeadBucket cannot verify Object Read & Write permissions. Services and backends remain independent of HTTP. |
| 2026-10-11 | SPEC-storage §11 | `make test-s3` and CI build a pinned MinIO release from source and run it in a temporary Docker container instead of depending on a published MinIO service image. | Both image registries failed to provide the pinned image in the implementation environment. Source installation keeps the exact release reproducible and does not add MinIO to Vela dependencies. |
| 2026-10-11 | SPEC-storage §5.4, §9, §12 | Presigned GET grants are capped at object/link expiry. No hybrid delivery or presigned PUT. R2 backup contains only SQLite; `--verify` is rejected, and README describes paired bucket backups with rclone. R2 with a nonempty accel prefix fails before serving. | Operator choices for v1; short-lived external grants must not extend the original authorization. |
| 2026-10-11 | SPEC-storage §9 | Migration verifies existing destinations before consulting source contents, so a repeated `--delete-source` pass resumes even after some source copies were removed. A backup lease pins blobs and serializes migration with backup and other migrations. | Existence is insufficient; verified copies make interrupted migration and cleanup safely resumable. Writes must still be quiesced by the operator. |
| 2026-10-11 | §9; SPEC-storage §7 | The collector dequeues only after successful physical deletion (or after observing a still-referenced blob). Failed removals remain queued and stop the current pass; a later pass retries without an orphan walk. | Fixes retry-state loss during backend outages and process crashes between selection and physical deletion. |
| 2026-10-11 | §12.3; SPEC-storage §5.4 | Client transport diagnostics omit raw errors/URLs while preserving causes through unwrap; redirects always strip Authorization and Referer, including with custom HTTP clients. R-001, R-003. | Presigned URLs and Vela keys must not leak through network errors or same-host storage redirects. |
| 2026-10-11 | SPEC-storage §5.4 | Conditional downloads in redirect mode use proxy (If-Match, If-None-Match, If-Range and timestamp conditions); HEAD/304 remain local. R-002. | S3 validators differ from Vela SHA-256 ETags; an external redirect cannot preserve a discarded Range or a satisfied Vela precondition. |
| 2026-10-11 | §10.4; SPEC-storage §5.1 | Resetting Content-Type reads at most 512 bytes before the write transaction, with a 10-second context covering detection/retries. The transaction checks object ID, blob ID and revision; a concurrent change retries, or fails the supplied If-Match. R-004. | Remote reads must not block unrelated SQLite writes or lease renewals; metadata must describe the inspected version. |
| 2026-10-11 | §13; SPEC-storage §5.3, §6, §9 | R2 upload/create limits must fit fixed part size × 10000. Put rejects unsupported limits before reading; migration preflights all referenced sizes before any copy, including dry-run. R-006. | Streaming wrappers prevent SDK size discovery and automatic part resizing; unsupported limits must fail before transferring thousands of parts. |
| 2026-10-11 | §11.3; SPEC-storage §5.4 | Invalid or unsatisfiable ranges in FS direct and R2 delivery return 416 with Cache-Control: no-store and no ETag, retaining Content-Range: bytes */size. R-008. | A cached Range error must not replace the normal representation under a URL-only shared cache key. Covered by a real nginx proxy-cache test. |

| 2026-10-11 | §0, §12–16; SPEC-storage §5–6 | Open source under MIT. Neutral examples in `examples/`; filesystem direct delivery is the default; accel requires explicit opt-in. Trusted proxies default to loopback and index title to Vela; login has no built-in URL. Docker Hub releases support amd64 and arm64. | Deployments must set their own origin, proxy trust, delivery mode, and branding; existing accel deployments must retain an explicit prefix. |
| 2026-10-11 | SPEC-storage §5–6 | `VELA_R2_REGION` defaults to `auto`; an explicit S3 endpoint does not require a Cloudflare account ID. | Other S3-compatible services can use their own signing region without adding another backend or renaming existing configuration. |
| 2026-10-11 | §0, §11, §15–18 | Stage 4 keeps README compact: overview, visibility, Getting Started, and deployment entry points. Code-linked technical references live in `docs/`; contributor verification stays in `CONTRIBUTING.md`. The full user manual belongs on `https://docs.xchunk.org/vela`, maintained separately. | Explicit documentation scope: keep implementation contracts near the code without duplicating a website or growing the root README into a manual. |

| 2026-10-12 | §11–12; SPEC-dirs | Directory policies are independent settings. Index/noindex inherit; hidden does not. SQLite generation triggers invalidate immutable in-memory policy snapshots across processes; recursive moves carry rules atomically. | Hidden entrances require navigation control without changing file authorization. |
| 2026-10-12 | SPEC-dirs §0, §5.2, §6 | Preserve original HTML and subtree file counts while no rules exist; with any rule use visible-entry counts. Public JSON adds title/description/noindex. Keep Page.Parent as a string instead of Parent.URL. | User-approved resolution of contradictory count/compatibility requirements; preserve the existing parent JSON field type. |
| 2026-10-12 | SPEC-dirs §8–9, §14–16 | Root DB rule overrides default-on VELA_ROOT_INDEX; empty non-root indexes are 404; disabled children omitted; source prefixes returned as-is. Policy-only prefixes can move. New conflict code covers policy target collisions; existing object codes remain. Random names and future access features deferred. | Accepted design decisions recorded in SPEC-dirs §16. Full user website remains separately maintained under Stage 4. |
| 2026-10-12 | SPEC-dirs §8.1, §9.3 | Recursive overwrite checks admin authorization for deleting an existing root policy as well as moving a source rule onto root. R-001. | Unrestricted rw must not re-enable an admin-disabled root through a move. |
| 2026-10-12 | SPEC-dirs §4–6 | Validate the persistent policy generation after all object-listing batches and retry on changes, including cross-process moves. R-002. | A hidden directory must not appear because a reader combines old rules with new object paths. |
| 2026-10-12 | SPEC-dirs §5.1 | Disabled and absent public indexes use a bounded existence lookup before any metadata listing/tree construction. Slashless directory redirects use the same lookup instead of full subtree statistics. R-003. | Avoid content-dependent 404 work and anonymous full-store scans for disabled roots. |
| 2026-10-12 | §12.3; SPEC-dirs §11 | Existing CLI long listings and directory URL/open commands detect a missing /dirs collection and retain legacy behavior with older API v1 servers. Individual 404s, authorization/transport errors, and supported-server disabled indexes do not bypass policy resolution. R-004. | Additive endpoints must not force synchronized CLI/server upgrades. |
