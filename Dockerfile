# syntax=docker/dockerfile:1

FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -o /out/gatekeeper ./cmd/gatekeeper
RUN go build -o /out/testupstream ./cmd/testupstream
RUN go build -o /out/jwksmock ./cmd/jwksmock

# distroless/static's nonroot variant already runs as an unprivileged
# user (65532:65532) by default - no separate USER setup needed.
FROM gcr.io/distroless/static-debian12:nonroot AS gatekeeper
COPY --from=build /out/gatekeeper /gatekeeper
ENTRYPOINT ["/gatekeeper"]

FROM gcr.io/distroless/static-debian12:nonroot AS testupstream
COPY --from=build /out/testupstream /testupstream
ENTRYPOINT ["/testupstream"]

FROM gcr.io/distroless/static-debian12:nonroot AS jwksmock
COPY --from=build /out/jwksmock /jwksmock
ENTRYPOINT ["/jwksmock"]
