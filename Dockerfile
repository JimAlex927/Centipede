FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/centipede-api ./cmd/api
RUN CGO_ENABLED=0 go build -trimpath -o /out/centipede-migrate ./cmd/migrate
RUN mkdir -p /runtime/logs /runtime/data/storage

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/centipede-api /usr/local/bin/centipede-api
COPY --from=build /out/centipede-migrate /usr/local/bin/centipede-migrate
COPY --from=build /src/config /config
COPY --from=build /src/migrations /migrations
COPY --from=build --chown=nonroot:nonroot /runtime /app
ENV CONFIG_DIR=/config
ENV DOTENV_PATH=/dev/null
ENV APP_ENV=docker
ENV CONFIG_SOURCE=file
EXPOSE 7788
ENTRYPOINT ["/usr/local/bin/centipede-api"]
