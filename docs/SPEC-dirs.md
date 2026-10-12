# Vela — дополнение к спецификации: политики директорий (скрытые директории и управление индексом)

> Дополнение к `docs/SPEC.md` (и последующим дополнениям). Положить в репо как `docs/SPEC-dirs.md`. Где этот документ противоречит предыдущим, **приоритет у этого документа**. Термины MUST / SHOULD / MAY — как в основном SPEC. Перед реализацией задать вопросы из §14.
>
> Документ самодостаточен: по нему должна быть возможна реализация без доступа к обсуждению, из которого он вырос.

---

## 0. TL;DR

Сейчас видимость есть только у **файлов** (`public` / `unlisted` / `private`), а директории неявные и попадают в индекс автоматически, если внутри есть public-файлы. Этого не хватает для сценария:

> Корень `store.example.com/` никому не показывать. Директорию `test0/` в корне не показывать, но если человек знает адрес `store.example.com/test0/`, он видит нормальный индекс со всеми public-файлами и поддиректориями внутри.

Решение — новая сущность **политика директории** (directory policy), привязанная к префиксу пути и **независимая от файлов**. У неё два главных флага:

- `index: on | off` — есть ли у директории HTML-индекс. **Наследуется** вниз по дереву.
- `hidden: true | false` — показывать ли директорию в индексе родителя. **Не наследуется**.

Плюс производный флаг `noindex` для поисковиков (по умолчанию включается под скрытыми директориями) и косметические `title` / `description`.

Без единого правила поведение **полностью совпадает** с текущим (обратная совместимость, MUST).

---

## 1. Цели и не-цели

### Цели
1. Выключать HTML-индекс у любой директории, включая корень.
2. Скрывать директорию из индекса родителя, сохраняя её собственный индекс доступным по прямому URL.
3. Не допускать утечки скрытых директорий через индекс родителя, хлебные крошки, `robots.txt`, JSON-индекс и поисковики.
4. Управлять политиками через API и CLI; политики переживают переименование директорий.
5. Полная обратная совместимость при отсутствии правил.

### Не-цели
- **Не** механизм защиты доступа. Скрытая директория — это неочевидность, а не авторизация: кто знает или угадает URL, видит её индекс. Для приватности — `private`-файлы и подписанные ссылки. Это MUST быть явно сказано в документации и в справке CLI.
- Политики **не** влияют на доступ к самим файлам: `public`-файл открывается по прямому URL независимо от политик директорий, в которых он лежит.
- Политики **не** влияют на аутентифицированный API (`GET /api/v1/objects` и т.д.): обладатель ключа видит всё в пределах своего scope/префикса.
- Пароли на директории, подписанные ссылки на директории, листинги «только для ключа» — вне скоупа (см. §14, вопрос 6).

---

## 2. Термины

| Термин | Значение |
|---|---|
| **Directory prefix** | Путь директории с завершающим `/`, например `test0/`, `test0/sub/`. Корень — пустая строка `""` (в URL и CLI обозначается `/`). |
| **Policy (правило)** | Явно сохранённая запись для одного префикса. Существует независимо от того, есть ли под префиксом объекты. |
| **Effective policy** | Итоговые значения флагов для директории с учётом наследования и дефолтов (§4). |
| **Visible entry** | Файл или поддиректория, которые отображаются на странице индекса директории (§5.2). |
| **Hidden subtree** | Директория со `hidden: true` и всё под ней. |

---

## 3. Модель данных политики

| Поле | Тип | Default (если правила нет) | Наследуется | Смысл |
|---|---|---|---|---|
| `index` | `on` \| `off` \| `inherit` | `inherit` | **да** | Есть ли у директории HTML-индекс. |
| `hidden` | bool | `false` | **нет** | Показывать ли директорию в индексе родителя. |
| `noindex` | `on` \| `off` \| `inherit` | `inherit` | **да** | Запрещать ли поисковикам индексировать страницы индекса и файлы под этой директорией (§7). |
| `title` | string ≤ 256 | `""` | нет | Заголовок страницы индекса директории (вместо имени). |
| `description` | string ≤ 4096 | `""` | нет | Описание, выводится на странице индекса (plain text, экранируется). |

Служебные поля записи: `prefix`, `created_at`, `updated_at`, `revision` (как у объектов: `ETag: "r<rev>"`, `If-Match`).

Ограничения:
- `prefix` валидируется теми же правилами, что пути объектов (`SPEC §4.1`: NFC, запрещённые символы, длины, зарезервированные первые сегменты), но MUST заканчиваться на `/` (кроме корня `""`).
- **Корень**: `hidden` для корня бессмыслен → попытка установить `hidden: true` на `""` → `400 invalid_request`.
- Правило может существовать для префикса, под которым нет объектов (заранее подготовленная директория), и для префикса, совпадающего с именем существующего **файла** плюс `/` (например, правило `a/` при существующем файле `a`). Это не ошибка: правило просто ни на что не влияет, пока директории `a/` не существует. Конфликты файл/директория (`SPEC §4.2`) касаются только объектов.

---

## 4. Вычисление эффективной политики

Для директории с префиксом `D`:

```
ancestors(D) = [D, parent(D), parent(parent(D)), ..., ""]   // nearest first, root last

effective.index =
    first rule r in ancestors(D) with r.index != inherit  → r.index
    otherwise                                              → config root default (VELA_ROOT_INDEX)
  // A DB rule on "" with index != inherit overrides VELA_ROOT_INDEX (see §10).

effective.hidden =
    rule(D).hidden if rule(D) exists, else false           // never inherited

effective.noindex =
    first rule r in ancestors(D) with r.noindex != inherit → r.noindex
    otherwise → "on" if any directory in ancestors(D) has effective.hidden == true
                else "off"

effective.title, effective.description = rule(D) fields or ""
```

Почему `hidden` не наследуется (MUST, ключевое решение): если бы наследовался, то `test0/sub/` стал бы скрытым и исчез из индекса `test0/`, хотя задача — скрыть только «дверь», а внутри всё обычно.

Почему `noindex` по умолчанию включается под скрытой директорией: иначе первая же ссылка на `test0/`, попавшая в интернет, приведёт поисковик внутрь, и смысл скрытия пропадёт.

### Пример

```
rules:
  ""           index: off
  test0/       index: on,  hidden: true
  test0/raw/   index: off
  photos/      hidden: true                 (index: inherit)

effective:
  ""                   index off                         → store.example.com/ → 404
  test0/               index on,  hidden, noindex on     → открывается по URL, в корне не показан
  test0/sub/           index on,  noindex on (inherited) → показан в индексе test0/
  test0/raw/           index off                         → 404, в индексе test0/ не показан;
                                                          файлы внутри доступны по прямым URL
  photos/              index off (inherited from ""), hidden
                                                         → 404; hidden ни на что не влияет,
                                                          т.к. корень и так без индекса
  docs/                index off (inherited)             → 404
```

### Производительность (MUST)

- Все правила загружаются в память при старте (их единицы–сотни) и хранятся в структуре для поиска по ближайшему предку (map по префиксу достаточно: глубина пути ограничена, проход по предкам — O(глубина)).
- Кэш инвалидируется при любой записи правила (в том же процессе — синхронно после коммита транзакции). Ручной `velad` с отдельным процессом, меняющий правила, MUST либо идти через API, либо сервер перечитывает правила по `PRAGMA data_version` / счётчику ревизий в `meta` перед использованием (выбрать и задокументировать).

---

## 5. Публичная часть: изменения в маршрутах и индексе

Дополняет и заменяет соответствующие правила `SPEC §11`.

### 5.1 Маршруты

| Запрос | Поведение |
|---|---|
| `GET /` | если `effective("").index == off` → **404**; иначе индекс корня |
| `GET /{dir}/` | если директория не существует (нет public-объектов в поддереве), **или** `effective.index == off` → **404**; иначе индекс |
| `GET /{dir}` (без `/`) | если директория существует **и** `effective.index == on` → `301` на `/{dir}/`; если существует, но `index == off` → **404** (не раскрывать существование); иначе — обычная логика для файла |
| `GET /{path}` для файла | **без изменений**: public/unlisted отдаются, private → 404. Политики директорий на доступ к файлу не влияют, только добавляют `X-Robots-Tag` по §7 |
| `GET /?format=json`, `GET /{dir}/?format=json` | те же правила, что и для HTML: при `index == off` → 404; содержимое фильтруется так же, как HTML (§5.2) |

«Директория существует» = есть хотя бы один `public`, не просроченный, не в корзине объект с этим префиксом (как и раньше). Пустая директория с правилом `index: on` → 404 (см. §14, вопрос 3).

Ответ 404 для «индекс выключен» и «директории нет» MUST быть неразличим (тот же шаблон, те же заголовки, сопоставимое время ответа без лишних запросов к БД в одной ветке).

### 5.2 Что показывается на странице индекса директории `D`

**Файлы**: прямые потомки `D` с `visibility = public`, не просроченные, не в корзине. (Как раньше.)

**Поддиректории**: прямая поддиректория `S` показывается, если одновременно:
1. `effective(S).hidden == false`;
2. `effective(S).index == on` (не показываем ссылку, которая ведёт в 404);
3. у `S` есть хотя бы одна visible entry (рекурсивно по тем же правилам) — то есть по ссылке пользователь увидит непустую страницу.

Счётчик элементов у поддиректории в индексе (`Dirs[].Count` в данных темы) = количество visible entries на странице `S` (файлы + поддиректории по правилам выше), а не общее число объектов в поддереве.

Условие 3 рекурсивное; реализация SHOULD вычислять его одним запросом по диапазону префикса `S` (все public-объекты поддерева) и последующей фильтрацией в памяти с использованием кэша правил, а не рекурсией запросов к БД.

**Дата изменения поддиректории** = максимальный `updated_at` среди объектов, участвующих в её visible entries (рекурсивно). Не должна раскрывать активность в скрытых поддеревьях: объекты под hidden-потомками в расчёт не идут.

### 5.3 Хлебные крошки и ссылка «наверх»

- Крошка для директории-предка `A` рендерится **ссылкой**, только если `effective(A).index == on`. Иначе — простым текстом (имя сегмента без ссылки). Для корня при `index: off` — текст `VELA_SITE_TITLE` без ссылки или вообще без крошки (решение темы; дефолтная тема — текст без ссылки).
- Скрытый предок (например, `test0/`, когда пользователь в `test0/sub/`) рендерится ссылкой, если у него `index: on` — человек и так пришёл оттуда, раскрытия нет.
- Ссылка «..» (parent) подчиняется тому же правилу.

### 5.4 `robots.txt`

- Скрытые директории MUST NOT перечисляться в `robots.txt` (`Disallow: /test0/` раскрыло бы их). Защита от поисковиков для них — только через `X-Robots-Tag` / `<meta name="robots">` (§7).
- Если `effective("").index == off`, `robots.txt` SHOULD содержать `Disallow: /` для корня? — **нет**: это запретило бы и индексацию public-файлов по прямым ссылкам. Поведение `robots.txt` не меняется, кроме запрета перечислять скрытые префиксы.

---

## 6. Данные для темы (изменения контракта `SPEC §11.2`)

Добавить/изменить:

- `Page.Title` — `effective.title` или имя директории (для корня — `VELA_SITE_TITLE`).
- `Page.Description` — `effective.description` (plain text).
- `Page.NoIndex` — bool; если true, шаблон `layout.html` MUST вывести `<meta name="robots" content="noindex, nofollow">` (дефолтная тема делает это; контракт темы документирует обязанность).
- `Breadcrumbs[].URL` — пустая строка, если крошка не должна быть ссылкой (§5.3). Шаблон рендерит текст вместо ссылки.
- `Parent.URL` — пустая строка, если ссылка наверх не нужна.
- `Dirs[]` — только visible поддиректории (§5.2); добавить `Title` и `Description` из их политик (если заданы) для отображения рядом с именем.

Изменение контракта темы — breaking для кастомных тем: отметить в CHANGELOG и в документации тем (кастомная тема, не обрабатывающая пустой `URL` у крошек, отрендерит пустые ссылки).

---

## 7. Поисковики: `noindex`

Если `effective(D).noindex == on`:
- страница индекса `D` отдаётся с `X-Robots-Tag: noindex, nofollow` и `<meta name="robots" content="noindex, nofollow">`;
- **public-файлы**, лежащие под `D` (у которых ближайшая директория имеет `noindex: on`), отдаются с `X-Robots-Tag: noindex, nofollow` — так же, как unlisted.
  - В accel-режиме — через тот же механизм `X-Vela-Robots` → `$upstream_http_x_vela_robots` (`SPEC §11.3`, `§14`).
  - В direct / r2-proxy режиме — заголовок напрямую.
  - В r2-redirect режиме — заголовок на ответе 302 (на ответ R2 повлиять нельзя; документировать как ограничение).
- Unlisted-файлы — без изменений (`noindex` всегда).
- `Cache-Control` для таких ответов не меняется.

Явное `noindex: off` на скрытой директории разрешено (пользователь осознанно хочет, чтобы поисковики индексировали скрытую ветку) — это валидная, хоть и редкая конфигурация.

---

## 8. Взаимодействие с операциями над объектами

### 8.1 Move (`POST /api/v1/move`)
- **Рекурсивный move** префикса `from/` → `to/` MUST в **той же транзакции** переносить все правила с префиксами, начинающимися на `from/` (включая само `from/`), заменяя `from/` на `to/`.
- Если под `to/` уже существуют правила (для `to/` или его потомков):
  - без `overwrite` → `409 conflict`, `details.conflicting_policies: [...]`, ничего не меняется;
  - с `overwrite` → существующие правила под `to/` удаляются, переносимые занимают их место.
- Нерекурсивный move одного файла правила **не трогает**.
- В ответе: `{"moved": 12, "policies_moved": 2}`.
- Ключ с префиксом: правила переносятся только если и источник, и цель внутри префикса ключа (это уже гарантирует проверка самого move).

### 8.2 Delete / trash / restore
- Удаление объектов (в т.ч. рекурсивное) правила **не удаляет**: если директория снова появится, правило продолжит действовать. Это осознанное решение: правило — настройка, а не данные.
- Restore объекта не создаёт и не меняет правила.
- Janitor правила **не** чистит. Для обзора «осиротевших» правил — `vela dir ls --orphaned` (§9.2) и поле `exists` в API (§9.1).

### 8.3 Upload / create
Без изменений. Новый public-файл в скрытой директории сразу появляется в её индексе и не появляется в индексе родителя.

---

## 9. API

### 9.1 DTO

```json
// DirPolicy — a stored rule
{
  "prefix": "test0/",
  "index": "on",
  "hidden": true,
  "noindex": "inherit",
  "title": "Test area",
  "description": "",
  "created_at": "2026-10-12T10:00:00Z",
  "updated_at": "2026-10-12T10:00:00Z",
  "revision": 2
}

// EffectiveDir — computed view of a directory
{
  "prefix": "test0/sub/",
  "rule": null,                         // DirPolicy or null if no own rule
  "effective": {
    "index": "on",
    "index_source": "test0/",           // prefix the value came from; "" for root rule; "config" for VELA_ROOT_INDEX
    "hidden": false,
    "noindex": "on",
    "noindex_source": "hidden:test0/",  // "<prefix>" for explicit rule, "hidden:<prefix>" when derived from a hidden ancestor, "default"
    "title": "",
    "description": ""
  },
  "exists": true,                       // has at least one public, non-expired object in subtree
  "url": "https://store.example.com/test0/sub/",  // null if the index page would 404
  "listed_in_parent": true              // would this dir appear on the parent's index page
}
```

`listed_in_parent` и `url` нужны, чтобы CLI мог честно сказать пользователю, что увидит посторонний.

### 9.2 Эндпоинты

Все под `/api/v1`, с авторизацией. Префикс в URL — с завершающим `/`; корень — `/api/v1/dirs/` (пустой `{prefix...}`). Сервер нормализует префикс (добавляет `/`, если его нет, и применяет §3 валидацию).

| Метод | Путь | Описание |
|---|---|---|
| `GET` | `/api/v1/dirs?prefix=&recursive=true&orphaned=false` | список **правил** под префиксом (по умолчанию все в пределах ключа). `orphaned=true` — только правила, у которых `exists == false`. Ответ: `{"policies":[DirPolicy...], "next_cursor":""}` с пагинацией как у objects |
| `GET` | `/api/v1/dirs/{prefix...}` | `EffectiveDir` для префикса (работает и без правила) |
| `PUT` | `/api/v1/dirs/{prefix...}` | создать или полностью заменить правило. Тело — поля DirPolicy без служебных; отсутствующие поля = дефолты из §3. `If-Match` опционально, `If-None-Match: *` — только создать. Ответ `200`/`201` + `EffectiveDir` |
| `PATCH` | `/api/v1/dirs/{prefix...}` | JSON Merge Patch существующего правила; если правила нет — создаёт с дефолтами + патч. `null` у поля → дефолт. Ответ `200` + `EffectiveDir` |
| `DELETE` | `/api/v1/dirs/{prefix...}` | удалить правило (директория возвращается к наследуемому поведению). `204`; нет правила → `404 not_found` |

Валидация тела: неизвестные поля → `400 invalid_request`; `index`/`noindex` вне enum → 400; `hidden: true` для корня → 400.

### 9.3 Права

| Scope | Права на политики |
|---|---|
| `ro` | `GET` |
| `rw` | всё, в пределах префикса ключа; правило **на сам префикс ключа** (например, `ci/` для ключа с префиксом `ci/`) разрешено |
| `admin` | всё; правило на **корень** `""` — только `admin` без ограничения префиксом |

Ключ с префиксом в `GET /api/v1/dirs` видит только правила внутри своего префикса; `GET /api/v1/dirs/{prefix}` для префикса вне его → `403`. Поле `index_source` / `noindex_source`, указывающее на предка вне префикса ключа, MAY возвращаться как есть (это не секрет для ключа, читающего свою ветку) — либо маскироваться; выбрать и задокументировать (§14, вопрос 5).

### 9.4 Коды ошибок
Используются существующие (`SPEC §10.3`); новый код не нужен. Конфликт правил при move — `409` с `code: "conflict"` и `details.conflicting_policies`.

---

## 10. Конфигурация

| Переменная | Default | Описание |
|---|---|---|
| `VELA_ROOT_INDEX` | `on` | дефолт `index` для корня, если в БД нет правила на `""` с `index != inherit` |

Приоритет: правило в БД на корень > `VELA_ROOT_INDEX`. Логика: env — дефолт инсталляции, БД — оперативная настройка через CLI без рестарта. При старте сервер логирует итоговое значение для корня и его источник. См. §14, вопрос 1.

---

## 11. CLI

Новая группа `vela dir`. Префиксы принимаются с завершающим `/` или без (CLI добавляет); корень — `/`.

| Команда | Описание |
|---|---|
| `vela dir ls [prefix] [--orphaned] [--json]` | таблица правил: `PREFIX`, `INDEX`, `HIDDEN`, `NOINDEX`, `TITLE`, `EXISTS`. Значения `inherit` показываются серым как `(inherit)` |
| `vela dir show <prefix> [--json]` | эффективная политика с источниками и итог «что увидит посторонний»: «Index page: https://…/test0/ (not listed in parent, hidden from search engines)» или «Index page: disabled (404)» |
| `vela dir set <prefix> [--index on\|off\|inherit] [--hidden \| --no-hidden] [--noindex on\|off\|inherit] [--title s] [--description s]` | PATCH правила; печатает результат как `dir show` |
| `vela dir rm <prefix> [-y]` | удалить правило; подтверждение для корня |
| `vela dir hide <prefix>` | шорткат: `--hidden --index on` (сценарий «скрытая дверь») |
| `vela dir private-root` | MAY, шорткат: `--index off` на корень с предупреждением, что public-файлы по прямым ссылкам продолжают открываться |

Изменения существующих команд:
- `vela ls -l` помечает поддиректории с правилами: `[hidden]`, `[no index]` (данные — из `GET /api/v1/dirs?prefix=<dir>&recursive=false`, отдельный запрос, контракт листинга объектов **не меняется**).
- `vela mv a/ b/` в выводе сообщает о перенесённых правилах.
- `vela url <dir>/` для директории печатает URL индекса или объясняет, почему его нет.

Справка `vela dir --help` и `vela dir hide --help` MUST содержать предупреждение: «Hidden directories are not access control. Anyone who knows or guesses the URL can see the index. Use a hard-to-guess name or private files with signed links for anything sensitive.»

MAY: `vela dir hide --random <parent>/` — создать правило для новой директории со случайным именем (например, 10 символов base32) и напечатать её URL. Удобно для «секретной ссылки на каталог».

Автодополнение префиксов — как у путей (§12.3 основного SPEC).

---

## 12. Хранилище и миграция

```sql
-- migration NNNN_dir_policies.sql
CREATE TABLE dir_policies (
  prefix       TEXT PRIMARY KEY,                 -- '' for root, otherwise ends with '/'
  idx          TEXT NOT NULL DEFAULT 'inherit' CHECK (idx IN ('on','off','inherit')),
  hidden       INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0,1)),
  noindex      TEXT NOT NULL DEFAULT 'inherit' CHECK (noindex IN ('on','off','inherit')),
  title        TEXT NOT NULL DEFAULT '',
  description  TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  revision     INTEGER NOT NULL DEFAULT 1,
  CHECK (prefix = '' OR substr(prefix, -1) = '/'),
  CHECK (NOT (prefix = '' AND hidden = 1))
);
```

- Миграция только добавляет таблицу; существующие данные не трогаются. Без правил поведение идентично предыдущей версии (MUST, покрыть тестом: снапшот индекса до/после миграции на одном наборе данных).
- `velad backup` включает таблицу автоматически (это та же БД).
- Слой: `internal/store` (репозиторий `DirPolicies`), `internal/service` (сервис `Dirs`: CRUD, вычисление effective, кэш), `internal/index` получает effective-политики через интерфейс, а не из store напрямую.
- Типы `DirPolicy`, `EffectiveDir`, enum `IndexMode` — в публичном пакете `api/`; методы — в `client/`.

---

## 13. Тесты (MUST)

**Вычисление политики (unit, таблицы):**
- наследование `index` через несколько уровней, переопределение на промежуточном уровне, `inherit` на промежуточном уровне;
- `hidden` не наследуется;
- `noindex`: производный от hidden-предка, явный `off` под hidden, явный `on` без hidden;
- корень: правило в БД vs `VELA_ROOT_INDEX`, оба варианта;
- отказ `hidden: true` для корня.

**Индекс (HTTP через httptest):**
- без правил — результат побайтно совпадает с поведением до фичи (golden-файлы);
- сценарий из §4: корень 404, `test0/` открывается и не виден в корне, `test0/sub/` виден в `test0/`, `test0/raw/` 404 и не виден в `test0/`;
- поддиректория без visible entries не показывается (в т.ч. если всё её содержимое под hidden-потомком);
- счётчик и дата поддиректории не учитывают скрытые поддеревья;
- `/{dir}` без слэша: 301 при `index on`, 404 при `index off`; ответ 404 для «нет директории» и «индекс выключен» идентичен;
- хлебные крошки и parent-ссылка не ведут на директории с `index off`;
- `?format=json` подчиняется тем же правилам;
- `X-Robots-Tag` и `<meta robots>` на страницах и public-файлах под hidden; отсутствие на обычных;
- `robots.txt` не содержит скрытых префиксов.

**API:**
- CRUD правил, `If-Match`/`If-None-Match`, merge patch, валидация;
- права по scope и префиксу ключа, корень только для admin без префикса;
- рекурсивный move переносит правила атомарно; конфликт правил без `overwrite` → 409 без изменений; с `overwrite` → замена;
- удаление объектов не удаляет правила; `orphaned=true` их находит.

**Кэш:** изменение правила через API немедленно отражается в индексе (без рестарта и без ожидания).

**CLI:** `dir set/show/ls/rm/hide` против in-process сервера, `--json`, пометки в `ls -l`.

---

## 14. Открытые вопросы

1. **Корень: env или БД?** Предложено: БД-правило на `""` переопределяет `VELA_ROOT_INDEX`. Альтернатива — только env (проще, но меняется рестартом) или только БД.
2. **Default `VELA_ROOT_INDEX`** для open source: `on` (как сейчас, предложено) или `off` (безопаснее)? Личная инсталляция в любом случае ставит `off`.
3. **Пустая директория с явным `index: on`**: 404 (предложено, единообразно) или пустая страница индекса с заголовком/описанием из правила (удобно, чтобы заранее «застолбить» страницу)?
4. **Поддиректории с `index: off` в индексе родителя**: скрывать (предложено, чтобы не было ссылок в 404) или показывать без ссылки (как «папка, в которую нельзя заглянуть»)?
5. **`index_source` вне префикса ключа**: возвращать как есть или маскировать?
6. **Расширения на будущее** (не в этой итерации, но не мешать архитектурой): пароль на директорию, подписанная ссылка на индекс директории, политика видимости по умолчанию для новых файлов в директории (`default_visibility` — например, всё, что кладётся в `test0/`, по умолчанию `public`).
7. **`vela dir hide --random`** — делать сейчас или позже?

---

## 15. Критерии готовности

- [ ] Без правил поведение и вывод индекса идентичны предыдущей версии (golden-тесты).
- [ ] Флаги `index` (наследуемый), `hidden` (ненаследуемый), `noindex` (наследуемый, производный от hidden), `title`, `description` работают по §3–§4.
- [ ] Скрытые директории не утекают через индекс родителя, счётчики/даты, хлебные крошки, `robots.txt`, JSON-индекс; поисковикам отдаётся `noindex`.
- [ ] 404 для «нет директории» и «индекс выключен» неразличимы.
- [ ] API `/api/v1/dirs` с правами по scope/префиксу; рекурсивный move атомарно переносит правила.
- [ ] CLI `vela dir …` с понятным выводом «что увидит посторонний» и предупреждением, что это не защита.
- [ ] Контракт темы обновлён, дефолтная тема поддерживает новые поля; изменения отмечены в CHANGELOG как breaking для кастомных тем.
- [ ] Документация (сайт) обновлена: концепция политик директорий, сценарий «скрытая дверь», ограничения.

## 16. Implementation decisions (2026-10-12)

Accepted before implementation:

- A root DB rule overrides `VELA_ROOT_INDEX`; the configuration default is `on`.
- A non-root prefix with no live public objects returns 404 even with `index: on`.
  The root retains its legacy empty index when enabled.
- Child directories with `index: off` are omitted from their parent's index.
- Effective policy source prefixes are returned unchanged, including ancestors
  outside an authenticated key's prefix.
- Passwords, directory signed links, default file visibility, and `hide --random`
  are deferred. `dir private-root` is included.

Resolved compatibility and implementation details:

- **No rules:** preserve the original HTML byte-for-byte, including subtree file
  counts. With any stored rule, counts use visible entries and the default theme
  labels them as entries. This resolves the conflict between §5.2 and the full
  compatibility requirement in §0/§13. Public JSON adds `title`, `description`,
  and `noindex`; its existing fields retain their original values without rules.
- **Parent:** retain the existing `Page.Parent` URL string and JSON string type.
  An empty string suppresses the parent link. The proposal's `Parent.URL` would
  replace an existing field's type and break public JSON compatibility.
- **Cache:** a persistent `counters.dir_policies` generation is incremented by
  SQLite triggers in every policy transaction. All rules load at startup; each
  operation checks this generation before using an immutable snapshot. The generation is also validated after all object-listing batches, retrying
  the listing when it changes. Local writes invalidate the cache synchronously
  after commit. This also detects
  edits made through another process/pool; pooled `PRAGMA data_version` is not
  used because its value is connection-local.
- **Move:** policy-only prefixes can move recursively. Destination conflicts
  cover every rule under the target, excluding rules already moving from the
  source (necessary for a move to an ancestor). Overwrite replaces that target
  rule set in the same transaction as objects. Every new prefix is validated;
  moving a hidden rule to root is invalid, and writing a root rule needs admin.
- **Errors:** existing code did not have the proposed `conflict` code; add it
  for this new policy conflict situation (HTTP 409, CLI exit 4). Existing object
  conflicts keep their previous codes.
- **PUT:** unknown fields, a non-object body, null fields, and explicit empty
  modes are invalid; omitted modes take `inherit`. PATCH null resets defaults.
  Revision preconditions also apply to PATCH and DELETE.
- **Delivery:** R2 redirect mode puts `X-Robots-Tag` on Vela's 302 only; Vela
  cannot set the header on the subsequent storage response. Crawler policies
  are not an access boundary in any delivery mode.
- **Documentation:** implementation contracts and the hidden-entrance scenario
  are documented in `docs/directories.md`. The separately maintained user
  website is outside this repository's documentation scope (AGENTS.md Stage 4).
