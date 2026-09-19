package admin

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seorilabs/platform/server/internal/httpx"
	"github.com/seorilabs/platform/server/internal/platformerr"
	"github.com/seorilabs/platform/server/internal/registry"
	"github.com/seorilabs/platform/server/internal/remoteconfig"
)

// maxBlockedVersions는 한 플랫폼에서 강제할 수 있는 버전 수다.
//
// 목록이 길어진다는 건 임계값으로 막고 싶다는 뜻이고, 그건 이 기능이
// 의도적으로 지원하지 않는 방식이다.
const maxBlockedVersions = 20

// updatePolicyRequest는 업데이트 정책 저장 요청이다.
//
// 안내 문구와 스토어 주소를 받지 않는다. 문구는 서버 상수이고 주소는
// 레지스트리다. 강제 업데이트 화면에 운영자가 오타를 넣을 수 있는 필드를
// 하나도 남기지 않는다.
type updatePolicyRequest struct {
	AppID string `json:"appId"`
	// Platforms는 android와 ios만 받는다. 이 요청이 정책 전체를 대체한다.
	Platforms map[string]updatePolicyEntry `json:"platforms"`
	// Confirmation은 강제 목록을 바꿀 때만 필요하다.
	// "BLOCK <appId> <platform> <version>" 형태를 정렬 순으로 이어 붙인다.
	Confirmation string `json:"confirmation,omitempty"`
}

type updatePolicyEntry struct {
	// 빈 배열과 빈 문자열이 "비운다"는 유효한 값이므로 pointer로 필드 누락과
	// 구분한다. maintenanceRequest.Minutes와 같은 이유다.
	BlockedVersions   *[]string `json:"blockedVersions"`
	RecommendOverride *string   `json:"recommendOverride"`
}

type updatePolicyPlatformView struct {
	// Configured는 Firestore에 이 플랫폼의 정책 항목이 실제로 있는지다.
	//
	// 정책이 없어도 자동 추종 값이나 스토어 주소가 있으면 플랫폼이 응답에
	// 실린다. 콘솔이 둘을 구분하지 못하면, 다른 플랫폼만 고치고 저장할 때
	// 전체 대체 요청이 이 플랫폼의 정책을 조용히 지운다.
	Configured        bool                 `json:"configured"`
	BlockedVersions   []blockedVersionView `json:"blockedVersions"`
	RecommendOverride string               `json:"recommendOverride,omitempty"`
	// AutoRecommendedVersion은 override가 없을 때 실제로 적용되는 값이다.
	AutoRecommendedVersion string `json:"autoRecommendedVersion,omitempty"`
	// UpdateURL은 이 플랫폼의 유저가 실제로 열게 될 주소다.
	// 아무도 막히기 전에 어디로 보내지는지 확인할 수 있어야 한다.
	UpdateURL string `json:"updateUrl,omitempty"`
}

type blockedVersionView struct {
	Version   string     `json:"version"`
	BlockedAt *time.Time `json:"blockedAt,omitempty"`
}

type observedVersionView struct {
	Platform    string    `json:"platform"`
	Version     string    `json:"version"`
	Runtime     string    `json:"runtime,omitempty"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
}

type updatePolicyResponse struct {
	AppID     string                              `json:"appId"`
	Platforms map[string]updatePolicyPlatformView `json:"platforms"`
	// ObservedVersions는 콘솔이 드롭다운으로 보여줄 후보다.
	// 자유 입력을 두면 운영자가 손으로 타이핑하고, 그게 사고를 만든다.
	ObservedVersions []observedVersionView `json:"observedVersions"`
}

// updatePolicy는 현재 정책과 고를 수 있는 버전을 함께 돌려준다.
//
// 읽기 없이 쓰기만 열면 콘솔이 현재 값을 못 보여주고 blind write만 하게 된다.
func (h *Handler) updatePolicy(w http.ResponseWriter, r *http.Request) error {
	appID := r.PathValue("appId")
	if !adminAppIDPattern.MatchString(appID) {
		return platformerr.New(platformerr.CodeRequestInvalid, "앱 식별자가 필요해요")
	}
	if h.config == nil {
		return platformerr.New(platformerr.CodeRuntimeConfigInvalid, "설정 서비스가 준비되지 않았어요")
	}
	app, err := h.apps.Get(r.Context(), appID)
	if err != nil {
		return err
	}

	policy, _, err := h.config.GetUpdatePolicy(r.Context(), appID)
	if err != nil {
		return err
	}
	observed, err := h.config.ObservedVersions(r.Context(), appID)
	if err != nil {
		return err
	}
	auto := remoteconfig.HighestObservedVersions(observed, h.now())

	platforms := map[string]updatePolicyPlatformView{}
	for _, platform := range updatePolicyPlatforms {
		entry, configured := policy.Platforms[platform]
		if !configured && auto[platform] == "" && app.UpdateURL(platform) == "" {
			continue
		}
		view := updatePolicyPlatformView{
			Configured:             configured,
			BlockedVersions:        []blockedVersionView{},
			RecommendOverride:      entry.RecommendOverride,
			AutoRecommendedVersion: auto[platform],
			UpdateURL:              app.UpdateURL(platform),
		}
		for _, blocked := range entry.BlockedVersions {
			item := blockedVersionView{Version: blocked.Version}
			if !blocked.BlockedAt.IsZero() {
				at := blocked.BlockedAt.UTC()
				item.BlockedAt = &at
			}
			view.BlockedVersions = append(view.BlockedVersions, item)
		}
		platforms[platform] = view
	}

	httpx.WriteOK(w, http.StatusOK, updatePolicyResponse{
		AppID:            appID,
		Platforms:        platforms,
		ObservedVersions: observedVersionViews(observed),
	})
	return nil
}

// updatePolicyPlatforms는 정책을 걸 수 있는 플랫폼이다.
//
// ait과 web은 없다. 미니앱 번들은 토스가 전달하고 웹은 새로고침이라
// 유저가 "설치본을 업데이트"할 대상 자체가 없다.
var updatePolicyPlatforms = []string{remoteconfig.PlatformAndroid, remoteconfig.PlatformIOS}

func observedVersionViews(observed []remoteconfig.ObservedAppVersion) []observedVersionView {
	out := make([]observedVersionView, 0, len(observed))
	for _, entry := range observed {
		out = append(out, observedVersionView{
			Platform:    remoteconfig.PlatformFromRuntime(entry.Runtime),
			Version:     entry.Version,
			Runtime:     entry.Runtime,
			FirstSeenAt: entry.FirstSeenAt.UTC(),
		})
	}
	// 최근에 들어온 빌드를 먼저 보여준다. 정렬을 Firestore에 걸면 복합
	// 인덱스가 필요하고, 인덱스 누락은 배포 후에야 500으로 드러난다.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FirstSeenAt.Equal(out[j].FirstSeenAt) {
			return out[i].FirstSeenAt.After(out[j].FirstSeenAt)
		}
		return out[i].Version > out[j].Version
	})
	return out
}

// setUpdatePolicy는 업데이트 정책을 저장한다.
//
// 요청이 정책 전체를 대체한다. 부분 병합을 하면 "지금 무엇이 막혀 있나"를
// 요청만 보고 알 수 없어 감사가 성립하지 않는다.
func (h *Handler) setUpdatePolicy(w http.ResponseWriter, r *http.Request) error {
	if h.config == nil {
		return platformerr.New(platformerr.CodeRuntimeConfigInvalid, "설정 서비스가 준비되지 않았어요")
	}

	var req updatePolicyRequest
	if err := httpx.DecodeStrict(w, r, &req); err != nil {
		return err
	}
	next, err := parseUpdatePolicyRequest(req)
	if err != nil {
		return err
	}

	app, err := h.apps.Get(r.Context(), req.AppID)
	if err != nil {
		return err
	}
	if err := app.EnsureUsable(); err != nil {
		return err
	}

	current, currentVersion, err := h.config.GetUpdatePolicy(r.Context(), req.AppID)
	if err != nil {
		return err
	}

	added := addedBlockedVersions(current, next)
	if len(added) > 0 {
		if got, want := req.Confirmation, blockConfirmation(req.AppID, added); got != want {
			return platformerr.New(platformerr.CodeRequestInvalid,
				"확인 문구가 정확하지 않아요: "+want)
		}
		// 관측 원장은 새 차단을 검증할 때만 필요하다. 무조건 읽으면
		// app_versions 조회 하나가 실패했다는 이유로 긴급 해제까지 막힌다.
		// 원장이 연결되지 않았으면 여기서 실패한다. 가드가 미조립 상태에서
		// 열려 있으면 안 된다.
		observed, err := h.config.ObservedVersions(r.Context(), req.AppID)
		if err != nil {
			return err
		}
		if err := assertBlockable(app, observed, next); err != nil {
			return err
		}
	}

	actor := ActorFrom(r.Context())
	login := actorLogin(actor)
	detail := map[string]any{"actor": login, "platforms": auditPlatforms(next)}
	if err := h.ledger.CheckAdminMutationRate(r.Context(), actor.Email); err != nil {
		if h.auditor != nil {
			h.auditor.Record(r.Context(), "config.update_policy", req.AppID, "",
				string(platformerr.CodeOf(err)), detail)
		}
		return err
	}
	// 가드를 통과시킨 그 정책이 그대로 현재일 때만 쓴다.
	if err := h.config.SetUpdatePolicy(
		r.Context(), req.AppID, next, currentVersion, login,
	); err != nil {
		return err
	}

	outcome := "cleared"
	if hasBlockedVersions(next) {
		outcome = "blocked"
	} else if hasRecommendOverride(next) {
		outcome = "recommended"
	}
	if h.auditor != nil {
		h.auditor.Record(r.Context(), "config.update_policy", req.AppID, "", outcome, detail)
	}

	httpx.WriteOK(w, http.StatusOK, map[string]any{
		"appId":     req.AppID,
		"outcome":   outcome,
		"platforms": auditPlatforms(next),
	})
	return nil
}

// parseUpdatePolicyRequest는 형식을 검증하고 정책으로 바꾼다.
//
// 하나라도 걸리면 아무것도 쓰지 않는다.
func parseUpdatePolicyRequest(req updatePolicyRequest) (remoteconfig.UpdatePolicy, error) {
	invalid := func(msg string) (remoteconfig.UpdatePolicy, error) {
		return remoteconfig.UpdatePolicy{}, platformerr.New(platformerr.CodeRequestInvalid, msg)
	}
	if !adminAppIDPattern.MatchString(req.AppID) {
		return invalid("앱 식별자가 필요해요")
	}
	if len(req.Platforms) == 0 || len(req.Platforms) > len(updatePolicyPlatforms) {
		return invalid("android 또는 ios 정책이 필요해요")
	}

	out := remoteconfig.UpdatePolicy{
		Platforms: make(map[string]remoteconfig.PlatformUpdatePolicy, len(req.Platforms)),
	}
	for platform, entry := range req.Platforms {
		if platform != remoteconfig.PlatformAndroid && platform != remoteconfig.PlatformIOS {
			return invalid("설치본이 없는 플랫폼에는 업데이트 정책을 걸 수 없어요: " + platform)
		}
		if entry.BlockedVersions == nil && entry.RecommendOverride == nil {
			return invalid(platform + " 정책에 바꿀 값이 없어요")
		}

		policy := remoteconfig.PlatformUpdatePolicy{}
		if entry.RecommendOverride != nil && *entry.RecommendOverride != "" {
			if !remoteconfig.ValidStableVersion(*entry.RecommendOverride) {
				return invalid("권장 기준 버전이 안정 SemVer가 아니에요: " + *entry.RecommendOverride)
			}
			policy.RecommendOverride = *entry.RecommendOverride
		}
		if entry.BlockedVersions != nil {
			versions := *entry.BlockedVersions
			if len(versions) > maxBlockedVersions {
				return invalid("한 플랫폼에서 강제할 수 있는 버전은 최대 " +
					strconv.Itoa(maxBlockedVersions) + "개예요")
			}
			for _, version := range versions {
				if !remoteconfig.ValidStableVersion(version) {
					return invalid("강제 대상 버전이 안정 SemVer가 아니에요: " + version)
				}
				for _, existing := range policy.BlockedVersions {
					if remoteconfig.SameVersion(existing.Version, version) {
						return invalid("강제 대상 버전이 중복됐어요: " + version)
					}
				}
				policy.BlockedVersions = append(policy.BlockedVersions,
					remoteconfig.BlockedVersion{Version: version})
			}
		}
		out.Platforms[platform] = policy
	}
	return out, nil
}

// assertBlockable은 강제 목록이 유저를 가둘 수 있는지 본다.
//
// 세 가지를 본다. 관측되지 않은 버전(오타), 탈출구 없음(더 높은 관측 버전이
// 없다), 전원 차단(관측된 안정 버전 전부를 덮는다).
func assertBlockable(
	app registry.App,
	observed []remoteconfig.ObservedAppVersion,
	next remoteconfig.UpdatePolicy,
) error {
	for _, platform := range updatePolicyPlatforms {
		policy := next.Platforms[platform]
		if len(policy.BlockedVersions) == 0 {
			continue
		}
		if app.UpdateURL(platform) == "" {
			return platformerr.New(platformerr.CodeUpdatePolicyInvalid,
				platform+" 스토어 주소가 레지스트리에 없어요. 갈 곳 없는 차단 화면을 만들 수 없어요")
		}

		known := observedVersionsFor(observed, platform)
		if len(known) == 0 {
			return platformerr.New(platformerr.CodeUpdateVersionUnknown,
				platform+"에서 관측된 빌드가 없어요")
		}

		blocked := make([]string, 0, len(policy.BlockedVersions))
		for _, entry := range policy.BlockedVersions {
			if !containsVersion(known, entry.Version) {
				return platformerr.New(platformerr.CodeUpdateVersionUnknown,
					"이 버전으로 세션이 열린 기록이 없어요: "+platform+" "+entry.Version)
			}
			blocked = append(blocked, entry.Version)
		}

		escape := ""
		for _, candidate := range known {
			if containsVersion(blocked, candidate) {
				continue
			}
			if escape == "" || remoteconfig.VersionLess(escape, candidate) {
				escape = candidate
			}
		}
		if escape == "" {
			return platformerr.New(platformerr.CodeUpdatePolicyInvalid,
				platform+" 관측된 모든 버전을 막게 돼요. 전면 중단은 점검 모드를 쓰세요")
		}
		highestBlocked := remoteconfig.HighestVersion(blocked)
		if !remoteconfig.VersionLess(highestBlocked, escape) {
			return platformerr.New(platformerr.CodeUpdatePolicyInvalid,
				platform+" "+highestBlocked+"보다 높은 관측된 버전이 없어요. 유저가 올라갈 곳이 없어요")
		}
	}
	return nil
}

func observedVersionsFor(
	observed []remoteconfig.ObservedAppVersion,
	platform string,
) []string {
	out := make([]string, 0, len(observed))
	for _, entry := range observed {
		if remoteconfig.PlatformFromRuntime(entry.Runtime) != platform {
			continue
		}
		if !remoteconfig.ValidStableVersion(entry.Version) {
			continue
		}
		if !containsVersion(out, entry.Version) {
			out = append(out, entry.Version)
		}
	}
	return out
}

func containsVersion(versions []string, version string) bool {
	for _, candidate := range versions {
		if remoteconfig.SameVersion(candidate, version) {
			return true
		}
	}
	return false
}

// addedBlockedVersions는 이번 요청으로 새로 막히는 버전이다.
//
// 이미 막혀 있던 버전을 그대로 두는 요청과 새로 막는 요청을 구분한다.
// 해제에는 확인 문구도 가드도 없다. 되돌리기는 언제나 즉시 가능해야 한다.
func addedBlockedVersions(current, next remoteconfig.UpdatePolicy) map[string][]string {
	added := map[string][]string{}
	for _, platform := range updatePolicyPlatforms {
		for _, entry := range next.Platforms[platform].BlockedVersions {
			already := false
			for _, previous := range current.Platforms[platform].BlockedVersions {
				if remoteconfig.SameVersion(previous.Version, entry.Version) {
					already = true
					break
				}
			}
			if !already {
				added[platform] = append(added[platform], entry.Version)
			}
		}
		sort.Strings(added[platform])
	}
	return added
}

// blockConfirmation은 운영자가 그대로 입력해야 하는 문구다.
//
// 무엇을 막는지 손으로 다시 쓰게 만든다. 목록 방식이라 문구에 버전이
// 전부 들어가고, 하나라도 다르면 요청이 거부된다.
func blockConfirmation(appID string, added map[string][]string) string {
	parts := make([]string, 0, 4)
	for _, platform := range updatePolicyPlatforms {
		for _, version := range added[platform] {
			parts = append(parts, "BLOCK "+appID+" "+platform+" "+version)
		}
	}
	return strings.Join(parts, "; ")
}

func hasBlockedVersions(policy remoteconfig.UpdatePolicy) bool {
	for _, entry := range policy.Platforms {
		if len(entry.BlockedVersions) > 0 {
			return true
		}
	}
	return false
}

func hasRecommendOverride(policy remoteconfig.UpdatePolicy) bool {
	for _, entry := range policy.Platforms {
		if entry.RecommendOverride != "" {
			return true
		}
	}
	return false
}

// auditPlatforms는 감사와 응답에 실을 요약이다. 값 자체가 감사 대상이므로
// 키 이름만 남기지 않는다.
func auditPlatforms(policy remoteconfig.UpdatePolicy) map[string]any {
	out := map[string]any{}
	for platform, entry := range policy.Platforms {
		versions := make([]string, 0, len(entry.BlockedVersions))
		for _, blocked := range entry.BlockedVersions {
			versions = append(versions, blocked.Version)
		}
		sort.Strings(versions)
		out[platform] = map[string]any{
			"blockedVersions":   versions,
			"recommendOverride": entry.RecommendOverride,
		}
	}
	return out
}
