# Контракт fork

Этот fork сохраняет поведение чистого upstream по умолчанию. Любое локальное
расширение обязано соблюдать правила ниже.

## Feature flags

- Каждая новая функция fork реализуется только за отдельным feature flag.
- Один flag имеет одну цель и не переиспользуется для другой функции. Для него
  заранее фиксируется план удаления либо явный постоянный статус
  `ops`/`permission` flag.
- Без явной конфигурации flag находится в безопасном состоянии `off`. Вызовы
  backend используют `false` как default; режим `off` сохраняет поведение
  чистого upstream.
- Backend — источник истины. Он закрывает flag-ом API, background jobs, side
  effects и security boundary. Скрыть только UI недостаточно.
- Если у функции есть UI, frontend использует тот же ключ flag только для
  presentation и не заменяет backend-проверку.

Штатная реализация находится в [`server/pkg/featureflag`](server/pkg/featureflag)
на backend и [`packages/core/feature-flags`](packages/core/feature-flags) на
frontend. Публичные frontend-ключи связываются через
[`server/internal/featureflags`](server/internal/featureflags). Этот документ
задаёт политику fork, но не дублирует технический контракт framework.

## Данные и миграции

- Миграции только additive и безопасны при `off`.
- Выключенная функция не создаёт новые данные и не меняет существующие.
- В PR описываются данные, создаваемые при `on`, их совместимость с `off` и
  поведение после rollback.

## Проверки и rollout

Каждый implementation PR обязан содержать:

- проверки режимов `off` и `on`, включая backend boundary и UI при его наличии;
- проверяемый rollback/kill-switch с быстрым возвратом в `off`;
- наблюдаемость решения flag и связанных side effects;
- план удаления flag либо обоснование его постоянного статуса.

Включение flag в production не входит в implementation PR. Это отдельное
решение rollout для exact head с smoke-проверкой, наблюдением и подтверждённой
возможностью быстро вернуть `off`.

## Upstream

Upstream sync никогда не смешивается с feature PR. Конфликты решаются в пользу
upstream-контракта; осознанное расхождение оформляется отдельным
документированным решением.
