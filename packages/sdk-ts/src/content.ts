import { Transport } from "./transport.ts";

export const CONTENT_SCHEMA_VERSION = 1 as const;

export type ContentAccess = "free" | "deep";
export type ContentScope = "base" | "seun" | "wolun";
export type SipseongFact =
  | "bigyeon" | "geopjae" | "siksin" | "sanggwan" | "pyeonjae"
  | "jeongjae" | "pyeongwan" | "jeonggwan" | "pyeonin" | "jeongin";
export type UnseongFact =
  | "jangsaeng" | "mogyok" | "gwandae" | "geonrok" | "jewang" | "soe"
  | "byeong" | "sa" | "myo" | "jeol" | "tae" | "yang";
export type OhaengStateFact = "과다" | "보통" | "부족";
export type SinsalNameFact =
  | "amrok" | "baekho_daesal" | "banan" | "biin" | "cheondeok_gwiin"
  | "cheondeok_hap" | "cheoneul_gwiin" | "cheonsa" | "cheonsal" | "cheonui_seong"
  | "eumyang_chachak" | "gasuk" | "geonrok" | "geopsal" | "geumyeo" | "geupgak"
  | "goegang" | "gongmang" | "goran" | "gosin" | "gugin_gwiin" | "gwangwi_hakgwan"
  | "gwimun_gwansal" | "gyeokgak" | "hakdang_gwiin" | "hongyeom" | "hwagae"
  | "hyeonchim" | "jaesal" | "jangseong" | "jisal" | "mangsin" | "muncheong_gwiin"
  | "mungok_gwiin" | "nakjeong_gwansal" | "nyeonsal_dohwa" | "samgi_gwiin"
  | "sipak_daepae" | "taegeuk_gwiin" | "woldeok_gwiin" | "woldeok_hap" | "wolsal"
  | "wonjin" | "yangin" | "yeokma" | "yukhae";

export interface ContentArticle {
  id: string;
  text: string;
  /** 같은 좌표의 원문. `text`가 압축본일 때만 온다 — `찬찬히 읽기` 접힘과 사전 본문. */
  more?: string;
  access: ContentAccess;
}

export interface ContentVersion {
  schemaVersion: number;
  contentVersion: string;
}

export interface FlowContentFact {
  sipseong: SipseongFact;
  state: OhaengStateFact;
}

export interface DerivedReadingFacts {
  kind: "full" | "three_pillar";
  chart: { year: string; month: string; day: string; hour?: string };
  ilju: string;
  johap: Array<{ sipseong: SipseongFact; unseong: UnseongFact }>;
  sinsal: Array<{ name: SinsalNameFact; variant?: "nyeonju" | "wolju" | "ilju" | "siju" | "outer" }>;
  relations: Array<{
    kind: "yukhap" | "samhap" | "banghap" | "chung" | "hyeong" | "jahyeong" | "pa" | "hae" | "wonjin" | "gwimun";
    pair?: string;
  }>;
  daeun: FlowContentFact[];
  seun: {
    year: number;
    flow: FlowContentFact;
    daeunSipseong: SipseongFact[];
    samjae?: "in" | "mid" | "out";
  };
  wolun: FlowContentFact[];
}

export interface ResolveContentRequest {
  schemaVersion: typeof CONTENT_SCHEMA_VERSION;
  reading: DerivedReadingFacts;
  scope: ContentScope[];
  unlock?: ContentUnlockRequest;
}

export type ContentUnlockRequest =
  | { section: "seun" | "wolun"; kind: "reward_claim"; claimId: string }
  | { section: "seun" | "wolun"; kind: "ticket"; claimId?: never };

export interface LockedDeepContent {
  deepKey: string;
  section: "seun" | "wolun";
  year: number;
}

export interface ResolvedContentReading extends ContentVersion {
  readingKey: string;
  articles: ContentArticle[];
  locked: LockedDeepContent[];
}

export interface ContentTerm extends ContentVersion {
  article: ContentArticle;
}

export type PairBranchRelation =
  | "yukhap" | "samhap" | "banghap" | "chung" | "hyeong" | "pa" | "hae" | "wonjin" | "gwimun";
export type PairOhaengName = "mok" | "hwa" | "to" | "geum" | "su";
/** `fill_b`는 B가 A의 빈 자리를 채운다는 뜻. 과다×과다는 overlap, 부족×부족은 gap. */
export type PairFillKind = "fill_a" | "fill_b" | "overlap" | "gap" | "plain";

/** 궁합 한쪽의 파생 명식. 생년월일·시각·이름은 없다. three_pillar면 hour가 없어야 한다. */
export interface PairingSideFacts {
  kind: "full" | "three_pillar";
  chart: { year: string; month: string; day: string; hour?: string };
}

/**
 * 앱 계산 코어가 두 명식에서 뽑은 짝 사실. 서버가 같은 값을 다시 유도해 대조한다 —
 * `aToB`는 B의 일간에서 본 A의 일간 십성(A가 B에게 무엇인가), `ilji.tags`는 고정 순서
 * (육합·삼합·방합·충·형·파·해·원진·귀문)의 두 일지 관계, `ohaeng`은 목·화·토·금·수 순서
 * 다섯 줄이다. 하나라도 다르면 `content_selector_invalid`다.
 */
export interface PairFacts {
  ilgan: { aToB: SipseongFact; bToA: SipseongFact; hap: boolean };
  ilji: { tags: PairBranchRelation[]; primary: PairBranchRelation | "same" | "none" };
  ohaeng: Array<{ name: PairOhaengName; a: OhaengStateFact; b: OhaengStateFact; kind: PairFillKind }>;
  close: { stem: "bihwa" | "sangsaeng" | "sanggeuk"; branch: "hap" | "chung" | "none" };
}

export type PairingUnlockRequest =
  | { section: "gunghap"; kind: "reward_claim"; claimId: string }
  | { section: "gunghap"; kind: "ticket"; claimId?: never };

export interface ResolveContentPairingRequest {
  schemaVersion: typeof CONTENT_SCHEMA_VERSION;
  a: PairingSideFacts;
  b: PairingSideFacts;
  pair: PairFacts;
  unlock?: PairingUnlockRequest;
}

export interface LockedPairingContent {
  deepKey: "gunghap";
  section: "gunghap";
}

export interface ResolvedContentPairing extends ContentVersion {
  /** 두 명식 해시를 정렬해 다시 해시한 값. A×B와 B×A가 같다. */
  pairKey: string;
  articles: ContentArticle[];
  /** 궁합은 전부 심화라 권한이 없으면 `gunghap` 하나가 잠긴다. */
  locked: LockedPairingContent[];
}

/** 인증·App Check가 필요한 private 콘텐츠 API. */
export class Content {
  constructor(
    private readonly transport: Transport,
    private readonly getToken: () => Promise<string>,
  ) {}

  async version(): Promise<ContentVersion> {
    return this.transport.request({
      method: "GET", path: "/v1/content/version", token: await this.getToken(),
    });
  }

  async resolve(request: ResolveContentRequest): Promise<ResolvedContentReading> {
    return this.transport.request({
      method: "POST", path: "/v1/content/readings:resolve",
      token: await this.getToken(), body: request,
    });
  }

  /**
   * 궁합 해설. 레지스트리에서 궁합이 꺼진 앱은 `content_not_enabled`(403), 구서버는
   * 404다 — 둘 다 "궁합 미제공"으로 다룬다.
   */
  async resolvePairing(request: ResolveContentPairingRequest): Promise<ResolvedContentPairing> {
    return this.transport.request({
      method: "POST", path: "/v1/content/pairings:resolve",
      token: await this.getToken(), body: request,
    });
  }

  async term(termId: string): Promise<ContentTerm> {
    if (!/^[a-z0-9가-힣][a-z0-9가-힣._-]{0,127}$/u.test(termId)) {
      throw new Error("termId 형식이 올바르지 않아요");
    }
    return this.transport.request({
      method: "GET", path: `/v1/content/terms/${encodeURIComponent(termId)}`,
      token: await this.getToken(),
    });
  }
}
