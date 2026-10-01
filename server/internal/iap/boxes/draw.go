package boxes

import (
	"crypto/rand"
	"math/big"
	"sort"

	"github.com/seorilabs/platform/server/internal/platformerr"
)

// Random은 추첨 난수 포트다. 운영은 CryptoRandom, 테스트는 고정 수열을 쓴다.
//
// 추첨은 서버 난수만 쓴다. 클라이언트가 결과를 미리 알 수 있는 시드를
// 공유하지 않는다(ADR 0029 4항).
type Random interface {
	// Intn은 [0, n) 정수를 돌려준다.
	Intn(n int) (int, error)
}

// CryptoRandom은 crypto/rand 기반 난수다.
type CryptoRandom struct{}

func (CryptoRandom) Intn(n int) (int, error) {
	if n <= 0 {
		return 0, platformerr.New(platformerr.CodeInternal, "추첨 범위가 올바르지 않아요")
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, platformerr.Wrap(err, platformerr.CodeInternal, "추첨 난수를 만들지 못했어요")
	}
	return int(v.Int64()), nil
}

// Draw는 한 번의 추첨 결과다.
type Draw struct {
	FriendID string
	Rarity   string
	// Pity는 보장 규칙으로 바뀐 결과라는 뜻이다.
	Pity bool
}

// Draw는 상자 하나를 추첨한다.
//
// 순서: 희귀도 가중 추첨 → 같은 희귀도 안 균등 → 보장.
// sinceRare는 마지막 보장 희귀도 이후 연 상자 수다. 이번 상자를 포함해
// pity개째인데 보장 희귀도가 아니면, 보장 희귀도 중 가장 적게 가진 친구
// (같으면 카탈로그 순서)를 준다. 앱의 core/friend_box.gd 와 같은 규칙이다.
func (v *Version) Draw(r Random, copies map[string]int, sinceRare int) (Draw, error) {
	roll, err := r.Intn(WeightTotal)
	if err != nil {
		return Draw{}, err
	}
	rarity := ""
	acc := 0
	for _, rr := range v.Rarities {
		acc += rr.Weight
		if roll < acc {
			rarity = rr.ID
			break
		}
	}
	pool := v.friendsOf(rarity)
	if len(pool) == 0 {
		return Draw{}, platformerr.New(platformerr.CodeRuntimeConfigInvalid, "추첨할 상자 친구가 없어요")
	}
	pick, err := r.Intn(len(pool))
	if err != nil {
		return Draw{}, err
	}
	out := Draw{FriendID: pool[pick].ID, Rarity: rarity}
	if rarity != v.PityRarity && sinceRare+1 >= v.Pity {
		best := v.friendsOf(v.PityRarity)[0]
		for _, f := range v.friendsOf(v.PityRarity) {
			if copies[f.ID] < copies[best.ID] {
				best = f
			}
		}
		out = Draw{FriendID: best.ID, Rarity: v.PityRarity, Pity: true}
	}
	return out, nil
}

// NextSinceRare는 이번 추첨 뒤의 보장 카운터다.
func (v *Version) NextSinceRare(sinceRare int, d Draw) int {
	if d.Rarity == v.PityRarity {
		return 0
	}
	return sinceRare + 1
}

func (v *Version) friendsOf(rarity string) []Friend {
	out := make([]Friend, 0, len(v.Friends))
	for _, f := range v.Friends {
		if f.Rarity == rarity {
			out = append(out, f)
		}
	}
	return out
}

// LevelForCopies는 누적 장수의 강화 레벨이다. 0장이면 0(없음).
// Lv L에는 누적 1 + L(L−1)/2장이 든다.
func LevelForCopies(copies int) int {
	if copies <= 0 {
		return 0
	}
	level := 1
	for copies >= CopiesForLevel(level+1) {
		level++
	}
	return level
}

// CopiesForLevel은 Lv L에 필요한 누적 장수다.
func CopiesForLevel(level int) int {
	return 1 + level*(level-1)/2
}

func sortStrings(s []string) { sort.Strings(s) }
