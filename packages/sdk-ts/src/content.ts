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
  unlock?: {
    section: "seun" | "wolun";
    kind: "reward_claim" | "ticket";
    claimId?: string;
  };
}

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
