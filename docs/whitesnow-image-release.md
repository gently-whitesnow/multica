# Выпуск Whitesnow container images v0.4.17

Workflow `Publish Whitesnow images v0.4.17` публикует два multi-platform
образа (`linux/amd64` и `linux/arm64`) в registry Whitesnow:

- `ghcr.io/gently-whitesnow/multica-backend`;
- `ghcr.io/gently-whitesnow/multica-web`.

Источник сборки зафиксирован внутри workflow: commit
`aa201cd6150a97bd5706b6cfb3333849aa840dcf`, версия `v0.4.17`. Значение ref
не принимается из ручного ввода и после checkout дополнительно сверяется с
`git rev-parse HEAD`. Поэтому повторный запуск собирает тот же source ref, даже
если default branch уже ушла вперёд.

## Повторный выпуск

После появления workflow в default branch запустите его без inputs:

```bash
gh workflow run whitesnow-images.yml --ref main
```

Первый запуск до merge выполняется bootstrap-тегом `images-v0.4.17`. Этот тег
только запускает workflow: build context всё равно берётся из зафиксированного
commit выше.

Workflow использует только штатный `GITHUB_TOKEN`; право `packages: write`
выдано только publish job. Ни application secrets, ни registry credentials не
передаются в Docker build args, labels или artifacts. BuildKit публикует
provenance attestation уровня `mode=max`; его build args содержат только
публичные version/revision/date значения.

## Получение и проверка digest

Полные immutable refs выводятся отдельно для backend и web в Summary завершённого
run. Тег `v0.4.17` удобен для поиска, но не является доказательством конкретного
артефакта: для эксплуатации и аудита используйте только `image@sha256:...`.

Проверка manifest не скачивает image layers и ничего не меняет в registry:

```bash
docker manifest inspect ghcr.io/gently-whitesnow/multica-backend@sha256:<backend-digest>
docker manifest inspect ghcr.io/gently-whitesnow/multica-web@sha256:<web-digest>
```

Для просмотра multi-platform состава и attestations используйте:

```bash
docker buildx imagetools inspect ghcr.io/gently-whitesnow/multica-backend@sha256:<backend-digest>
docker buildx imagetools inspect ghcr.io/gently-whitesnow/multica-web@sha256:<web-digest>
```

OCI labels каждого platform image содержат:

- `org.opencontainers.image.source=https://github.com/gently-whitesnow/multica`;
- `org.opencontainers.image.revision=aa201cd6150a97bd5706b6cfb3333849aa840dcf`;
- `org.opencontainers.image.version=v0.4.17`.

Точные labels platform image можно прочитать без запуска контейнера командой
`docker buildx imagetools inspect IMAGE@DIGEST --format '{{json .Image}}'`.
Workflow не выполняет deploy и не обращается к VPS или production runtime.
