FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/vm-fixtures ./cmd/vm-fixtures

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/vm-fixtures /usr/local/bin/vm-fixtures
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/vm-fixtures"]
