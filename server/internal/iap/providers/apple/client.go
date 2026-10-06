package apple

import "github.com/seorilabs/platform/server/pkg/appleapi"

// Apple 서명·인증서 폐기 확인은 게임 서버도 사용하는 공용 경계에 둔다.
// 구매 유형과 원장 정책은 이 provider에 남긴다.
type Client = appleapi.Client
type Config = appleapi.Config

var NewClient = appleapi.NewClient
