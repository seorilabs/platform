# Cloud Run은 amd64 전용이다. 배포 이미지는 GitHub-hosted 러너에서 만든다.
#
# 한동안 Cloud Build로 위임했었다. ARC RPi 러너의 네트워크 때문이었는데
# golang 이미지 pull과 go mod download가 각각 6분·9분이었고, Docker를
# 걷어내고 직접 빌드해도 모듈 다운로드만 20분을 넘겨 30분 timeout에
# 잘렸다. 저장소가 public이 되어 러너를 ubuntu-latest로 옮기면서 그
# 제약이 사라졌다. 러너도 amd64라 크로스컴파일이 없다.
#
# 로컬 검증은 네이티브 아키텍처로 한다.
#
#   docker build -t platform server
#
# 빌드 스테이지는 BUILDPLATFORM에 고정하고 GOARCH로 크로스컴파일한다.
# 고정하지 않으면 arm64 로컬에서 --platform linux/amd64를 줄 때 컴파일러까지
# QEMU로 돌아 Go 어셈블러가 세그폴트한다. 실제로 그렇게 실패한다.
#
# 예전에 이걸 못 쓴 이유는 Cloud Build의 빌더가 BuildKit이 아니어서
# BUILDPLATFORM 인자를 해석하지 못했기 때문이다. 빌드가 buildx로 오면서
# 그 제약이 없어졌다. CGO가 꺼져 있어 크로스컴파일에 툴체인이 더 필요하지
# 않다.

# go.mod의 go 지시어와 맞춘다. 낮으면 빌드가 거부된다.
FROM --platform=$BUILDPLATFORM golang:1.25 AS build

# buildx가 --platform에서 채워 준다. 직접 넘기지 않는다.
ARG TARGETARCH

WORKDIR /src

# 의존성 레이어를 먼저 캐시한다. 소스만 바뀌면 이 레이어는 재사용된다.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 이어야 정적 바이너리가 나오고 distroless/static 에서 돈다.
# -s -w 는 디버그 심볼을 빼 이미지를 줄인다.
#
# GOARCH는 buildx가 채우는 TARGETARCH를 따른다. 빌드 스테이지는
# BUILDPLATFORM에 고정돼 있으므로 둘이 다를 수 있고, 그 차이가 곧
# 크로스컴파일이다. 여기를 비워 두면 러너 아키텍처가 그대로 바이너리에
# 들어가 대상 base 이미지와 어긋난다.
#
# CI는 amd64 러너에서 amd64를 만들어 결과적으로 네이티브다. 크로스 경로는
# arm64 로컬에서 --platform linux/amd64로 빌드할 때 탄다.
RUN CGO_ENABLED=0 GOOS=linux GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /out/platform ./cmd/platform

# static-debian12 는 CA 인증서를 포함한다. scratch 는 포함하지 않아
# 외부 HTTPS 호출(마켓 API, JWKS)이 실패한다.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/platform /platform

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/platform"]
