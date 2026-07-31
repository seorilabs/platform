# Cloud Run은 amd64 전용이다. Go는 크로스컴파일이 네이티브라
# ARC arm64 러너에서도 QEMU 없이 amd64 바이너리를 만든다.
# 그래서 이 Dockerfile은 빌드 플랫폼과 무관하게 동작한다.

# go.mod의 go 지시어와 맞춘다. 낮으면 빌드가 거부된다.
FROM golang:1.25 AS build

WORKDIR /src

# 의존성 레이어를 먼저 캐시한다. 소스만 바뀌면 이 레이어는 재사용된다.
COPY go.mod go.su[m] ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 이어야 정적 바이너리가 나오고 distroless/static 에서 돈다.
# -s -w 는 디버그 심볼을 빼 이미지를 줄인다.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/platform ./cmd/platform

# static-debian12 는 CA 인증서를 포함한다. scratch 는 포함하지 않아
# 외부 HTTPS 호출(마켓 API, JWKS)이 실패한다.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/platform /platform

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/platform"]
