package identity

import "testing"

// 지원 코드 규칙을 고정한다.
//
// 유저가 화면에서 읽은 코드로 CS가 원장을 찾는다. 규칙이 바뀌면 이미
// 저장된 사용자 문서의 코드와 앱이 보여주는 코드가 갈라지고, 그 순간
// 문의 대응이 성립하지 않는다. 바꾸려면 마이그레이션이 먼저다.
func TestNewSupportCode(t *testing.T) {
	tests := []struct {
		name  string
		appID string
		puid  string
		want  string
	}{
		{
			name:  "대시 구분 앱은 각 단어 첫 글자를 딴다",
			appID: "lizard-tycoon",
			puid:  "pu_01KZ0E9KF04DBX8BRE0SJ2CK8C",
			want:  "LT-0SJ2CK8C",
		},
		{
			name:  "단일 단어 앱은 한 글자 접두사다",
			appID: "moonmate",
			puid:  "pu_01KZ0E9KF04DBX8BRE0SJ2CK8C",
			want:  "M-0SJ2CK8C",
		},
		{
			name:  "밑줄도 구분자다",
			appID: "happy_farm",
			puid:  "pu_01KZ0E9KF04DBX8BRE0SJ2CK8C",
			want:  "HF-0SJ2CK8C",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewSupportCode(tt.appID, tt.puid); got != tt.want {
				t.Errorf("NewSupportCode(%q, %q) = %q, want %q",
					tt.appID, tt.puid, got, tt.want)
			}
		})
	}
}

// 세션 응답과 사용자 문서가 같은 코드를 써야 한다.
//
// 앱이 보여주는 코드로 Admin 조회가 성립하지 않으면 이 기능은 없는 것과
// 같다. 조합 지점을 NewSupportCode 하나로 묶은 이유다.
func TestSupportCodeIsStable(t *testing.T) {
	const appID = "lizard-tycoon"
	const puid = "pu_01KZ0E9KF04DBX8BRE0SJ2CK8C"

	first := NewSupportCode(appID, puid)
	second := NewSupportCode(appID, puid)
	if first != second {
		t.Fatalf("같은 입력에 다른 코드가 나왔다: %q vs %q", first, second)
	}
	if !adminSupportCodeShape(first) {
		t.Errorf("Admin 조회 패턴과 맞지 않는다: %q", first)
	}
}

// Admin API가 조회에 쓰는 형식과 같은지 본다.
// server/internal/admin/handler.go의 adminSupportCodePattern과 맞춘다.
func adminSupportCodeShape(code string) bool {
	dash := -1
	for i, r := range code {
		if r == '-' {
			dash = i
			break
		}
	}
	if dash < 1 || dash > 3 || len(code)-dash-1 != 8 {
		return false
	}
	for _, r := range code[:dash] {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for _, r := range code[dash+1:] {
		found := false
		for _, c := range crockford {
			if r == c {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
