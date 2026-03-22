# Шаблоны промптов и сценарии работы агента

## Что такое prompt template

Prompt template - это переиспользуемый шаблон поведения агента, который подключается к задаче и влияет на то, как именно агент должен работать. Если task описывает содержание работы, то prompt template описывает сценарий исполнения.

Такие шаблоны нужны, чтобы не повторять вручную одно и то же поведение в каждой задаче:

- сначала задать вопросы;
- сначала показать план;
- не делать предположений без подтверждения;
- работать только как reviewer;
- выполнять задачу в максимально строгом режиме.

## Чем prompt template отличается от task template

### Task template

Task template стандартизирует поля задачи:

- какие данные нужно заполнить;
- какие guidelines прикрепить;
- какие команды validation задать по умолчанию.

### Prompt template

Prompt template стандартизирует поведение агента:

- должен ли агент сначала уточнять постановку;
- можно ли начинать реализацию без ответов пользователя;
- нужен ли обязательный план;
- в каком формате вернуть результат;
- можно ли только анализировать без изменения кода.

Обе сущности дополняют друг друга и могут использоваться вместе.

## Как prompt template подключается к задаче

### Добавление в каталог проекта

```bash
agentctl prompt-templates add ./prompts/clarify-then-implement.yml
agentctl prompt-templates list
```

### Подключение при создании задачи

```bash
agentctl task create \
  --title "Implement refresh token repository" \
  --goal "Add repository for refresh tokens in auth module" \
  --agent claude \
  --prompt-template clarify-then-implement \
  --prompt-template strict-executor
```

### Подключение к уже созданной задаче

```bash
agentctl task update TASK-001 --prompt-template clarify-then-implement
```

## Двухэтапный сценарий с вопросами перед реализацией

Это один из самых полезных шаблонов для реальной инженерной работы.

Сценарий выглядит так:

1. Пользователь создает задачу и прикрепляет шаблон `clarify-then-implement`.
2. При первом `task run` агент изучает задачу, scope, guidelines и контекст.
3. Агент не меняет код, а формирует список блокирующих вопросов.
4. Задача переходит в статус `awaiting_answers`.
5. Пользователь отвечает через CLI.
6. Система добавляет ответы в context pack и повторно строит prompt.
7. После `task resume` агент переходит к реализации.

### Команды для этого потока

```bash
agentctl task run TASK-001
agentctl task questions TASK-001
agentctl task answer TASK-001 --file answers.yml
agentctl task resume TASK-001
```

## Формат prompt template

Практичнее всего хранить prompt templates как `.yml` файлы с метаданными и телом шаблона. Тогда их можно и читать человеком, и обрабатывать программно.

Пример:

```yaml
id: clarify-then-implement
title: Clarify before implementation
description: Ask blocking questions before making any code changes.
mode: two_stage

behavior:
  clarification_required: true
  block_execution_until_answers: true
  max_questions: 5
  allow_non_blocking_assumptions: false

output:
  clarification_artifact: questions.md
  execution_artifacts:
    - diff.patch
    - summary.md
    - changed_files.json
    - validation.json

template: |
  You are responsible for executing the assigned engineering task.
  Before making any code changes, review the task, scope, constraints,
  guidelines and provided context.
  If anything important is ambiguous, ask up to 5 blocking questions.
  Do not modify files until answers are provided.
  After answers are available, continue with implementation and return the
  required artifacts.
```

## Самые полезные сценарии prompt templates

Ниже приведен набор шаблонов, который выглядит базовым и практически необходимым для первой версии продукта.

### `clarify-then-implement`

Назначение:
Агент обязан сначала задать блокирующие вопросы, если постановка или ограничения недостаточно ясны.

Когда нужен:
- задачи с неполным контекстом;
- рискованные изменения в production-коде;
- работы, где нельзя позволить агенту делать скрытые предположения.

Что дает:
- снижает количество неверных реализаций;
- переводит риск из стадии кода в стадию вопросов;
- делает выполнение двухэтапным и более контролируемым.

### `plan-then-implement`

Назначение:
Сначала агент строит краткий план реализации и impact analysis, а код начинает менять только после подтверждения.

Когда нужен:
- архитектурные изменения;
- рефакторинг нескольких модулей;
- работы с высокой стоимостью ошибки.

Что дает:
- раннюю проверку стратегии;
- возможность остановить неверный подход до кодовых изменений;
- прозрачность по шагам и зонам риска.

### `strict-executor`

Назначение:
Агент должен работать максимально дисциплинированно, без лишних предположений и без выхода за scope.

Когда нужен:
- продовые изменения;
- задачи с жесткими правилами слоев;
- сценарии с обязательным соблюдением guidelines.

Что дает:
- минимизацию лишних изменений;
- повышение предсказуемости;
- короткий и стандартизированный итоговый отчет.

### `research-only`

Назначение:
Агент не меняет код, а только анализирует систему и предлагает варианты решения.

Когда нужен:
- перед стартом большой задачи;
- для архитектурного исследования;
- когда сначала нужен анализ, а не реализация.

Что дает:
- безопасный режим исследования;
- понятный план без риска случайных правок;
- подготовку к последующей исполнительной задаче.

### `reviewer`

Назначение:
Шаблон для второго агента, который не пишет код, а анализирует diff, validation и соответствие guidelines.

Когда нужен:
- при включенном reviewer mode;
- на критичных изменениях;
- когда нужна независимая оценка результата.

Что дает:
- отдельный сценарий ревью без смешения ролей;
- фиксацию замечаний и рисков;
- более качественную приемку.

## Как prompt templates влияют на жизненный цикл задачи

Подключенный шаблон может добавлять промежуточные стадии в pipeline. Для двухэтапного режима типичны статусы:

- `clarifying`
- `awaiting_answers`

После ответа пользователя задача возвращается к обычному потоку подготовки и выполнения.

## Что важно предусмотреть в первой версии

Для MVP достаточно следующих возможностей:

1. Хранить prompt templates в каталоге проекта.
2. Подключать их к задаче через CLI.
3. Поддерживать как минимум один двухэтапный сценарий с вопросами.
4. Сохранять вопросы и ответы как артефакты run.
5. Позволять Prompt Builder собирать итоговый prompt из task spec, guidelines, context pack и prompt templates.
