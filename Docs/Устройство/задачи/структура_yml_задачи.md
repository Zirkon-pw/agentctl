# YML-структура задачи

## Назначение task YML

Task-файл фиксирует задачу в машинно-читаемом виде. Он нужен для того, чтобы постановка была воспроизводимой и не зависела от истории сообщений.

В этой документации используется расширение `.yml`, но логика одинаково применима и к `.yaml`.

## Минимальная структура задачи

Ниже приведена минимальная структура, напрямую вытекающая из README:

```yaml
id: TASK-001
title: Implement refresh token repository
goal: Add repository for refresh tokens in auth module
status: draft
agent: claude

prompt_templates:
  builtin:
    - clarify_if_needed
  custom: []

scope:
  allowed_paths:
    - src/Auth/**
  forbidden_paths:
    - src/Billing/**
    - frontend/**

guidelines:
  - backend-guidelines
  - ddd-rules

context:
  include_files:
    - src/Auth/Application/Interfaces/IRefreshTokenRepository.cs
    - src/Auth/Infrastructure/DependencyInjection.cs
  include_patterns:
    - RefreshToken
    - TokenRepository

constraints:
  no_breaking_changes: true
  require_tests: true

interaction:
  clarification_strategy: by_yml_files

clarifications:
  pending_request: null
  attached: []

runtime:
  max_execution_minutes: 45
  heartbeat_interval_sec: 5
  graceful_stop_timeout_sec: 20
  allow_pause: true
  allow_force_kill: true

validation:
  commands:
    - dotnet build
    - dotnet test
```

## Обязательные поля

### `id`

Уникальный идентификатор задачи, например `TASK-001`.

### `title`

Короткое и понятное название задачи.

### `goal`

Инженерная цель в одном-двух предложениях. Это главное объяснение, зачем задача вообще создается.

### `status`

Текущий этап жизненного цикла задачи. На момент создания обычно `draft`.

Рекомендуемый набор значений:

- `draft`
- `queued`
- `preparing_context`
- `running`
- `needs_clarification`
- `ready_to_resume`
- `paused`
- `stopping`
- `stopped`
- `killed`
- `validating`
- `review`
- `completed`
- `failed`
- `rejected`
- `canceled`

### `agent`

Идентификатор исполнителя, который будет использоваться для run, например `claude`, `codex` или `qwen`.

## Блок `prompt_templates`

Список шаблонов промпта, подключенных к задаче. Эти шаблоны задают режим поведения агента поверх самой постановки.

Пример:

```yaml
prompt_templates:
  builtin:
    - clarify_if_needed
    - strict_executor
  custom:
    - custom_backend_guard
```

Через этот блок задача может явно требовать, чтобы агент:

- запросил уточнение по задаче;
- сначала построил план;
- работал в строгом режиме;
- ограничился review или исследованием.

## Блок `scope`

`scope` ограничивает область работы агента.

### `allowed_paths`

Пути, в которых агенту разрешено вносить изменения.

### `forbidden_paths`

Пути, которые нельзя трогать. Это один из важнейших защитных механизмов системы.

### `must_read`

Рекомендуемое расширение структуры. Список файлов, которые агент обязан изучить перед выполнением.

Пример:

```yaml
scope:
  allowed_paths:
    - src/Auth/Application/**
    - src/Auth/Infrastructure/**
  forbidden_paths:
    - src/Payments/**
    - deployment/**
  must_read:
    - docs/backend-guidelines.md
    - src/Auth/Application/LoginHandler.cs
    - src/Auth/Infrastructure/DependencyInjection.cs
```

## Блок `guidelines`

Список правил проекта, которые обязательно учитываются при работе над задачей. Обычно это ссылки на документы из `.agentctl/guidelines/`.

## Блок `context`

`context` управляет тем, что именно попадет в context pack.

### `include_files`

Точные файлы, которые нужно передать агенту.

### `include_patterns`

Ключевые паттерны поиска по коду, например имя сущности или интерфейса.

### Допустимые расширения

Для более точной постановки можно дополнительно использовать:

- `include_tests`
- `include_docs`
- `include_summaries`

Эти поля не описаны как жесткая схема в README, но логично следуют из идеи Context Builder.

## Блок `constraints`

Описывает правила выполнения, которые нельзя нарушать.

На основе README здесь особенно уместны:

- `no_breaking_changes`
- `require_tests`
- `require_migration_review`
- `read_only_analysis`

## Блок `interaction`

`interaction` задает или переопределяет сценарий выполнения задачи. Чаще всего этот блок формируется на основе шаблона промпта, но при необходимости может содержать task-specific override.

Пример:

```yaml
interaction:
  clarification_strategy: by_yml_files
```

Через него удобно фиксировать:

- что уточнение оформляется отдельными `.yml` файлами;
- что задача может быть возвращена в работу после прикрепления clarification file;
- что поведение исполнения определяется шаблонами, а не ручными чат-командами.

## Блок `clarifications`

`clarifications` хранит ссылки на отдельные `.yml`-уточнения по задаче.

Пример:

```yaml
clarifications:
  pending_request: .agentctl/clarifications/TASK-001/clarification_request_001.yml
  attached:
    - .agentctl/clarifications/TASK-001/clarification_001.yml
```

Этот блок нужен, чтобы уточнения были отделены от основной задачи, но оставались частью воспроизводимого task flow.

## Блок `runtime`

`runtime` задает task-specific правила наблюдения и управления уже существующей задачей во время исполнения.

Пример:

```yaml
runtime:
  max_execution_minutes: 45
  heartbeat_interval_sec: 5
  graceful_stop_timeout_sec: 20
  allow_pause: true
  allow_force_kill: true
```

Через него удобно задавать:

- ожидаемый таймаут исполнения;
- частоту heartbeat;
- время ожидания graceful stop;
- можно ли паузить задачу;
- разрешен ли force kill для конкретного task type.

## Блок `validation`

`validation.commands` содержит команды, которые система должна запустить после выполнения задачи.

Пример:

```yaml
validation:
  commands:
    - dotnet build
    - dotnet test tests/Auth.UnitTests
    - dotnet test tests/Auth.IntegrationTests
```

## Рекомендуемые расширения для практической эксплуатации

README описывает еще несколько аспектов, которые разумно оформить прямо в task YML.

### `type`

Тип задачи, полезный для routing policy.

### `mode`

Режим исполнения, например `strict`, `fast` или `research`.

### `review`

Настройки reviewer-этапа.

Пример:

```yaml
review:
  required: true
  reviewer: codex
```

### `result`

Ожидаемые артефакты и требования к итоговому отчету.

Пример:

```yaml
result:
  expected_artifacts:
    - diff.patch
    - summary.md
    - changed_files.json
    - validation.json
```

## Расширенный пример task YML

Ниже пример более полной спецификации, которая объединяет идеи README в одном документе:

```yaml
id: TASK-001
title: Implement refresh token repository
goal: Add persistence for refresh tokens in auth module
status: draft
type: code_generation
agent: claude
mode: strict

prompt_templates:
  builtin:
    - clarify_if_needed
    - strict_executor
  custom: []

scope:
  allowed_paths:
    - src/Auth/Application/**
    - src/Auth/Infrastructure/**
  forbidden_paths:
    - src/Billing/**
    - frontend/**
  must_read:
    - docs/backend-guidelines.md
    - src/Auth/Application/LoginHandler.cs
    - src/Auth/Application/Interfaces/IRefreshTokenRepository.cs
    - src/Auth/Infrastructure/DependencyInjection.cs

guidelines:
  - backend-guidelines
  - ddd-rules

context:
  include_files:
    - src/Auth/Application/Interfaces/IRefreshTokenRepository.cs
    - src/Auth/Infrastructure/DependencyInjection.cs
  include_patterns:
    - RefreshToken
    - TokenRepository
  include_tests:
    - tests/Auth.UnitTests/**

constraints:
  no_breaking_changes: true
  require_tests: true
  require_migration_review: true

interaction:
  clarification_strategy: by_yml_files

clarifications:
  pending_request: null
  attached: []

runtime:
  max_execution_minutes: 45
  heartbeat_interval_sec: 5
  graceful_stop_timeout_sec: 20
  allow_pause: true
  allow_force_kill: true

validation:
  commands:
    - dotnet build
    - dotnet test tests/Auth.UnitTests
    - dotnet test tests/Auth.IntegrationTests

review:
  required: true
  reviewer: codex

result:
  expected_artifacts:
    - diff.patch
    - summary.md
    - changed_files.json
    - validation.json
```

## Что важно зафиксировать в любой задаче

Независимо от размера схемы, task YML должен отвечать на семь вопросов:

1. Что именно нужно сделать.
2. Где это можно делать.
3. Какие правила нужно соблюдать.
4. Какие встроенные или пользовательские шаблоны поведения подключены.
5. Нужны ли отдельные `.yml`-уточнения по задаче.
6. Что нужно прочитать перед работой.
7. Как проверить и принять результат.
