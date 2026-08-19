# Cloud Run은 amd64 전용이다. 이미지는 Cloud Build에서 만든다.
#
# 러너에서 만들던 구조는 ARC RPi 러너의 네트워크에 걸렸다. golang 이미지
# pull과 go mod download가 각각 6분·9분이었고, Docker를 걷어내고 러너에서
# 직접 빌드해도 모듈 다운로드만 20분을 넘겨 30분 timeout에 잘렸다. Cloud
# Build는 amd64 네이티브라 크로스컴파일도 필요 없고, 러너는 빌드를
# 제출하기만 한다.
#
# 로컬 검증은 네이티브 아키텍처로 한다.
#
#   docker build -t platform server
#
# arm64 로컬에서 --platform linux/amd64를 주면 컴파일러까지 QEMU로 돌아
# Go 어셈블러가 세그폴트한다. 예전에는 FROM --platform=$BUILDPLATFORM으로
# 그 경로를 막았지만, Cloud Build의 빌더는 BUILDKIT이 아니라서 그 인자를
# 해석하지 못한다. 배포 이미지는 amd64 네이티브인 Cloud Build가 만든다.

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
