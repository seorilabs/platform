# Cloud Run은 amd64 전용이다. 바이너리는 이 이미지 밖에서 만들고 여기서는
# 담기만 한다.
#
# 안에서 컴파일하던 구조는 ARC RPi 러너에서 30분 timeout에 걸렸다. DIND는
# 실행마다 빈 컨테이너라 golang 이미지 pull(6분)과 go mod download(9분)를
# 매번 처음부터 했다. 러너에서 직접 빌드하면 Go 모듈·빌드 캐시가 남고,
# 이 이미지는 2MB 남짓한 base에 바이너리 하나를 얹는 일만 한다.
#
# RUN이 없으므로 arm64 호스트에서 amd64 이미지를 만들 때도 QEMU가 필요 없다.
#
# 빌드 전에 amd64 정적 바이너리를 준비한다.
#
#   mkdir -p out
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
#     go build -trimpath -ldflags="-s -w" -o out/platform ./cmd/platform
#   docker build --platform linux/amd64 -t platform server

# static-debian12 는 CA 인증서를 포함한다. scratch 는 포함하지 않아
# 외부 HTTPS 호출(마켓 API, JWKS)이 실패한다.
FROM gcr.io/distroless/static-debian12:nonroot

COPY out/platform /platform

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/platform"]
