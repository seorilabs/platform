module github.com/seorilabs/platform/server

go 1.24

require cloud.google.com/go/firestore v1.18.0

// 나머지 의존성은 `go mod tidy`가 채운다.
// 직접 추가하지 말고 코드에서 import한 뒤 tidy를 돌린다.
