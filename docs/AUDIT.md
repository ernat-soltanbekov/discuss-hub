# Карта требований и аудит

Эта карта сопоставляет пункты предоставленного `discuss-hub__ts.md` с реализацией. Прохождение автоматических проверок не заменяет живую демонстрацию и объяснение решений. Полного обещания «любой возможный аудит пройдёт» здесь нет: приведены воспроизводимые проверки и их границы.

| Требование / бонус | Реализация | Проверка |
| --- | --- | --- |
| Структура проекта | `cmd/server`, `internal/{auth,db,handlers,insights}`, `templates`, `static/css` | `go run ./cmd/server -init-only` создаёт `forum.db`; `go build ./cmd/server` |
| Только разрешённые пакеты | stdlib, mattn/go-sqlite3, golang.org/x/crypto/bcrypt, gofrs/uuid/v5 | `go list -deps ./cmd/server`; `go.mod`, `go.sum` |
| SQLite CREATE/INSERT/SELECT | `db/schema.sql`, `db.go`, `auth.go`, `insights/stats.go` | `TestSchemaPersistenceAndConstraints` |
| Email, username, password | `auth.Register`, `register.html` | `TestRegistrationValidationAndStorage` |
| Дубли email/username | UNIQUE + COLLATE NOCASE; понятный HTTP 409 | `TestConcurrentRegistrationHonorsUniqueness`, HTTP journey |
| Ошибочный/пустой вход | общий ответ HTTP 401 | `TestInvalidCredentialsAndDatabaseFailure`, HTTP journey |
| Только одна сессия, срок | UUID cookie, hashed token, PRIMARY KEY user_id, expires_at | `TestSessionIsolationReplacementExpiryAndLogout`, `TestConcurrentLoginLeavesOneSession` |
| Другой браузер не получает права | пользователь в контексте отдельного запроса | `TestFullAuditJourneyAndBrowserIsolation` |
| Только зарегистрированные пишут и реагируют | `requireUser` на всех изменяющих маршрутах | `TestPublicPagesAndStatusCodes` |
| Пост + одна/несколько категорий | `CreatePost` с транзакцией | `TestInvalidPostsRollBackCompletely`, HTTP journey |
| Комментарии публичны | `GET /posts/{id}` | HTTP journey, гостевой просмотр |
| Пустые посты/комментарии запрещены | TrimSpace, длина, UTF-8, CHECK | `TestInvalidPostsRollBackCompletely`, `TestCommentReactionsAndMissingTargets` |
| Лайки/дизлайки постов и комментариев | две таблицы, первичный ключ (target,user), value ±1 | `TestFiltersReactionsAndPagination`, `TestCommentReactionsAndMissingTargets` |
| Счётчики доступны гостю | `post.html`, публичные карточки | ручная проверка в гостевом браузере + HTTP journey |
| Фильтр категории/своих/понравившихся | `db.Filter`, SQL EXISTS, ID из сессии | `TestFiltersReactionsAndPagination`, `TestValidationXSSSQLInjectionAndPrivateFilterScope` |
| Верные HTTP-методы и ошибки | ServeMux с GET/POST, 303 после успеха | `TestPublicPagesAndStatusCodes`, `TestDatabaseOutageReturns500AndHealth503` |
| Docker build/run | multi-stage Dockerfile, Compose, том | `scripts/container-smoke.sh`, CI job `container` |
| Публичный `/insights` | `analytics.go` | гостевой запрос в HTTP journey |
| Sentiment labels/formula/case | `insights/sentiment.go` | `TestSentimentFormula`, `FuzzSentimentInvariant` |
| Mood percentages | все посты, безопасное пустое состояние | `TestEmptyDashboard`, `TestSQLStatsNoJoinMultiplicationAndTopFive` |
| Most Active Category/User | SQL GROUP BY и CTE | `TestSQLStatsNoJoinMultiplicationAndTopFive`, `TestActiveUserCombinesPostsAndCommentsAndStableTies` |
| Top 5 likes + dislikes | SQL COUNT с детерминированной сортировкой | `TestSQLStatsNoJoinMultiplicationAndTopFive` |
| Sentiment каждого поста | paginated Recent Post Sentiment + метки в Top 5 | `TestRecentSentimentPagination` |
| Бонус: bcrypt | bcrypt.GenerateFromPassword / CompareHashAndPassword | проверка реального хеша в auth tests |
| Бонус: UUID пользователя | uuid.NewV4 | проверка версии UUID в auth tests |
| Бонус: `/insights/trending` | 24h, title+content, stop words, top 10 | `TestTrendingBoundariesStopWordsCountsAndTies`, `TestTrendingTopTenAndDatabaseErrors` |
| Бонус: эффективность | индексы, WAL, connection pool, пагинация, SQL-агрегация | нагрузочные тесты, benchmark, `EXPLAIN QUERY PLAN` |
| Понятный README и good practices | пакетные границы, форматирование, пояснения и ошибки | README, ARCHITECTURE, `go vet`, `gofmt` |

## Ручной сценарий за 10–15 минут

1. Запустите на **пустой** отдельной базе: `DB_PATH=data/audit.db go run ./cmd/server`.
2. Гостем откройте `/`, `/insights`, `/insights/trending`. Пустые состояния должны быть осмысленными; доли настроения — 0%.
3. Зарегистрируйте `audit_alice` и войдите. Попытки с тем же email или username должны показать ошибку. Пустой пароль/неверные данные не дают вход.
4. Откройте второй независимый браузер или приватный профиль. Он должен оставаться гостем. Войдите в нём **тем же аккаунтом**: первый после обновления теряет права. Два разных аккаунта не выталкивают друг друга.
5. Создайте пост с двумя категориями, содержимым `great love bad hate`. Пустой или состоящий из пробелов пост отклоняется. Добавьте обычный и пустой комментарий — второй должен быть отклонён.
6. Поставьте посту лайк, затем дизлайк. Должен остаться только дизлайк. Повторите на комментарии. Проверьте Remove reaction.
7. Проверьте все фильтры; личные фильтры относятся только к текущему аккаунту. Перейдите на второй аккаунт и сравните результаты.
8. На `/insights` созданный пост имеет **Neutral**, счёт 2 − 2. Создайте Positive (`great good`) и Negative (`broken awful`), проверьте доли.
9. На trending повторите слово в заголовке и тексте; оно должно учитываться в обоих местах. Для пограничного времени используйте unit-тест, а не ожидание суток.
10. Перезапустите приложение: пользователи, посты, реакции и неистёкшие сессии сохраняются. Уже открытые формы после рестарта нужно обновить из-за нового CSRF-ключа.
11. Выполните Docker build/run и `scripts/container-smoke.sh` при работающем Docker.

## Проверка SQLite вручную

Используйте ту же базу, что указана в `DB_PATH`:

```sh
sqlite3 data/audit.db
```

```sql
.headers on
.mode column
SELECT id, email, username, created_at FROM users;
SELECT id, user_id, title, content FROM posts;
SELECT * FROM post_categories;
SELECT * FROM comments;
SELECT user_id, expires_at FROM sessions;
SELECT * FROM post_reactions;
PRAGMA integrity_check;
PRAGMA foreign_key_check;
EXPLAIN QUERY PLAN
SELECT title, content FROM posts WHERE created_at >= 0 AND created_at <= 9999999999;
```

Не публикуйте содержимое рабочей таблицы sessions и password_hash в логах/скриншотах. Для проверки хеширования достаточно убедиться, что поле хранит bcrypt-хеш, а не введённую строку.

## Дополнительные проверки устойчивости

- 32 конкурентных писателя × 8 итераций: 256 комментариев и 256 смен реакций, после чего `integrity_check` и `foreign_key_check`.
- 16 параллельных HTTP-клиентов × 10 циклов: создание комментария и чтение согласованного dashboard.
- 12 одновременных входов одного аккаунта: остаётся один действующий токен.
- 8 одновременных регистраций одинакового пользователя: успешна одна.
- Удержание блокировки писателя: ограниченное ожидание, отсутствие частичного поста, успешная запись после снятия блокировки.
- Закрытая база: HTTP 500 на страницах данных, HTTP 503 на health; сессия не превращается молча в гостевую.
- Повреждённый файл или неизвестная новая версия схемы: отказ запуска с диагностикой.
- Вредоносный HTML, SQL-подобные строки, поддельный CSRF, чужой Origin, дубли полей, слишком большой запрос и исчерпание лимитов.
- Длинный Unicode на допустимой границе, пустое настроение, сортировка равных значений и точная граница 24 часов.

Актуальный результат запуска этих проверок: [VALIDATION.md](VALIDATION.md).
