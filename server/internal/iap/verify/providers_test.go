package verify

import (
	"testing"

	"github.com/seorilabs/platform/server/internal/iap/domain"
	"github.com/seorilabs/platform/server/internal/iap/providers/apple"
	"github.com/seorilabs/platform/server/internal/iap/providers/play"
	"github.com/seorilabs/platform/server/internal/iap/providers/toss"
)

// 마켓 검증기 3종이 Verifier를 만족하는지 컴파일 타임에 못 박는다.
//
// providers는 이 패키지를 import하지 않는다. Go의 암묵적 인터페이스라
// 시그니처가 어긋나도 조립하는 순간까지 아무도 모른다.
// 그 순간이 배포 시점이면 늦다.
//
// 테스트 파일에만 두어서 런타임 의존 방향은 그대로 유지한다.
var (
	_ Verifier = (*play.Verifier)(nil)
	_ Verifier = (*apple.Verifier)(nil)
	_ Verifier = (*toss.Verifier)(nil)
)

// 검증기는 Platform으로 라우팅된다. 셋이 서로 다른 값을 내야
// 맵에서 덮어쓰이지 않는다.
func TestVerifierPlatformsAreDistinct(t *testing.T) {
	got := map[domain.Platform]string{}

	for _, tc := range []struct {
		name string
		p    domain.Platform
	}{
		{"play", (&play.Verifier{}).Platform()},
		{"apple", (&apple.Verifier{}).Platform()},
		{"toss", (&toss.Verifier{}).Platform()},
	} {
		if prev, dup := got[tc.p]; dup {
			t.Errorf("%s와 %s가 같은 마켓 %q를 쓴다", prev, tc.name, tc.p)
		}
		got[tc.p] = tc.name
	}

	// 도메인이 아는 마켓과 검증기가 있는 마켓이 어긋나면
	// 어느 한쪽이 조용히 빠진다.
	//
	// 기준은 MarketPlatforms다. 운영자 지급은 백오피스가 근거를 갖고
	// 원장에 직접 쓰므로 마켓에 물어볼 것이 없다.
	for _, p := range domain.MarketPlatforms() {
		if _, ok := got[p]; !ok {
			t.Errorf("마켓 %q에 검증기가 없다", p)
		}
	}

	// 운영자 지급에는 검증기가 있으면 안 된다.
	// 있다면 외부 마켓 경로로 새고 있다는 뜻이다.
	if name, ok := got[domain.PlatformOperator]; ok {
		t.Errorf("운영자 지급에 %s 검증기가 붙었다", name)
	}
}

// MarketPlatforms는 AllPlatforms에서 운영자만 뺀 것이어야 한다.
// 한쪽에 마켓을 추가하고 다른 쪽을 잊으면 조용히 어긋난다.
func TestMarketPlatformsMatchesAll(t *testing.T) {
	markets := make(map[domain.Platform]bool)
	for _, p := range domain.MarketPlatforms() {
		if !p.IsMarket() {
			t.Errorf("%q가 MarketPlatforms에 있는데 IsMarket이 false다", p)
		}
		markets[p] = true
	}

	for _, p := range domain.AllPlatforms() {
		if p == domain.PlatformOperator {
			continue
		}
		if !markets[p] {
			t.Errorf("%q가 MarketPlatforms에서 빠졌다", p)
		}
	}

	if domain.PlatformOperator.IsMarket() {
		t.Error("운영자 지급이 마켓으로 분류됐다")
	}
}
