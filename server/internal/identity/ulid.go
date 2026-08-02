package identity

import (
	"crypto/rand"
	"fmt"
	"time"
)

// platform_user_id는 "pu_" + ULID다.
//
// firebase_uid에서 파생하지 않는다. 나중에 계정 병합이나 이관이 필요할 때
// 매핑만 바꾸면 원장을 건드리지 않아도 되기 때문이다. ADR 0008 참고.
//
// ULID를 쓰는 이유는 시간 순 정렬이 되면서 충돌하지 않기 때문이다.
// UUID v4는 정렬이 안 되고, 정렬 안 되는 문서 ID는 Firestore에서
// 핫스팟을 만들 수 있다.
const platformUserPrefix = "pu_"

// crockford는 ULID가 쓰는 Base32 알파벳이다.
// 사람이 헷갈리는 I, L, O, U를 뺐다.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewPlatformUserID는 새 platform_user_id를 만든다.
func NewPlatformUserID() (string, error) {
	u, err := newULID(time.Now())
	if err != nil {
		return "", err
	}
	return platformUserPrefix + u, nil
}

// newULID는 26자 ULID를 만든다.
//
// 48비트 밀리초 타임스탬프 + 80비트 난수 = 128비트를 Base32로 26자에 담는다.
func newULID(t time.Time) (string, error) {
	var b [16]byte

	ms := uint64(t.UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	if _, err := rand.Read(b[6:]); err != nil {
		return "", fmt.Errorf("identity: 난수 생성 실패: %w", err)
	}

	return encodeCrockford(b), nil
}

// encodeCrockford는 128비트를 26자로 인코딩한다.
//
// 26 * 5 = 130비트라 2비트가 남는다. ULID 스펙대로 첫 글자가
// 상위 3비트만 쓴다. 그래서 첫 글자는 항상 '7' 이하다.
func encodeCrockford(b [16]byte) string {
	out := make([]byte, 26)

	out[0] = crockford[(b[0]&224)>>5]
	out[1] = crockford[b[0]&31]
	out[2] = crockford[(b[1]&248)>>3]
	out[3] = crockford[((b[1]&7)<<2)|((b[2]&192)>>6)]
	out[4] = crockford[(b[2]&62)>>1]
	out[5] = crockford[((b[2]&1)<<4)|((b[3]&240)>>4)]
	out[6] = crockford[((b[3]&15)<<1)|((b[4]&128)>>7)]
	out[7] = crockford[(b[4]&124)>>2]
	out[8] = crockford[((b[4]&3)<<3)|((b[5]&224)>>5)]
	out[9] = crockford[b[5]&31]
	out[10] = crockford[(b[6]&248)>>3]
	out[11] = crockford[((b[6]&7)<<2)|((b[7]&192)>>6)]
	out[12] = crockford[(b[7]&62)>>1]
	out[13] = crockford[((b[7]&1)<<4)|((b[8]&240)>>4)]
	out[14] = crockford[((b[8]&15)<<1)|((b[9]&128)>>7)]
	out[15] = crockford[(b[9]&124)>>2]
	out[16] = crockford[((b[9]&3)<<3)|((b[10]&224)>>5)]
	out[17] = crockford[b[10]&31]
	out[18] = crockford[(b[11]&248)>>3]
	out[19] = crockford[((b[11]&7)<<2)|((b[12]&192)>>6)]
	out[20] = crockford[(b[12]&62)>>1]
	out[21] = crockford[((b[12]&1)<<4)|((b[13]&240)>>4)]
	out[22] = crockford[((b[13]&15)<<1)|((b[14]&128)>>7)]
	out[23] = crockford[(b[14]&124)>>2]
	out[24] = crockford[((b[14]&3)<<3)|((b[15]&224)>>5)]
	out[25] = crockford[b[15]&31]

	return string(out)
}

// NewSupportCode는 app_id와 platform_user_id에서 지원 코드를 만든다.
//
// 접두사 규칙과 코드 규칙을 한 자리에서 묶는다. 세션 응답과 사용자 문서가
// 각자 조합하면 한쪽만 바뀌었을 때 조용히 갈라지고, 그러면 유저가 화면에서
// 읽은 코드로 CS가 원장을 찾지 못한다.
func NewSupportCode(appID, platformUserID string) string {
	return SupportCode(supportPrefix(appID), platformUserID)
}

// SupportCode는 platform_user_id에서 CS용 지원 코드를 만든다.
//
// 앱 설정 화면에서 유저가 복사해 문의에 첨부한다.
// 이메일 검색이 성립하지 않는 이유는 플랫폼이 PII를 저장하지 않기 때문이다.
// ADR 0005 참고.
//
// ULID 뒤쪽 8자를 쓴다. 앞쪽은 타임스탬프라 같은 시기 가입자끼리 비슷하다.
// 뒤쪽은 난수라 짧아도 잘 갈린다.
func SupportCode(appPrefix, platformUserID string) string {
	id := platformUserID
	if len(id) > len(platformUserPrefix) {
		id = id[len(platformUserPrefix):]
	}
	if len(id) < 8 {
		return appPrefix + "-" + id
	}
	return appPrefix + "-" + id[len(id)-8:]
}
