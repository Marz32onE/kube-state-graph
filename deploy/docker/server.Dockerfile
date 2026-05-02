FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/kube-state-graph ./cmd/kube-state-graph

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/kube-state-graph /usr/local/bin/kube-state-graph
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/kube-state-graph"]
