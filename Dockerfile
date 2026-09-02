FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/centipede-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/centipede-api /centipede-api
COPY --from=build /src/config /config
ENV CONFIG_DIR=/config
EXPOSE 7788
ENTRYPOINT ["/centipede-api"]
