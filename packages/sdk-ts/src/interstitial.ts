/** SDK・인증・보상 원장에 의존하지 않는 자연 완료 경계 전면광고 제어. */
export interface InterstitialPolicy {
  version: string;
  enabled: boolean;
  cooldown_s: number;
  session_cap: number;
  daily_cap: number;
  session_reset_s: number;
  completions_between: number;
}
export interface InterstitialState {
  day: string;
  daily: number;
  session: number;
  reservedSession: number;
  reservedDaily: number;
  lastActivity: number;
  lastClosed: number;
  remainingCompletions: number;
  seen: string[];
  pending: string | null;
}
export interface InterstitialStore {
  read(): InterstitialState | null;
  write(state: InterstitialState): void;
}
export type InterstitialEvent = 'requested' | 'show' | 'impression' | 'clicked' | 'dismissed' | 'failedToShow' | 'error';
export function normalizeInterstitialEvent(type: string): InterstitialEvent | null {
  return ['requested', 'show', 'impression', 'clicked', 'dismissed', 'failedToShow', 'error'].includes(type)
    ? type as InterstitialEvent : null;
}
export interface CompletionOpportunity {
  id: string;
  placement: string;
  saved: boolean;
  confirmed: boolean;
  tutorialComplete: boolean;
  adRemoved: boolean;
}
export interface InterstitialAdapter {
  ready(): boolean;
  show(emit: (event: InterstitialEvent) => void): () => void;
}
export interface InterstitialOptions {
  policy: InterstitialPolicy;
  store: InterstitialStore;
  adapter: InterstitialAdapter;
  now?: () => number;
  track?: (name: string, params: Record<string, string | number>) => void;
  suspended?: (value: boolean) => void;
}
export class InterstitialController {
  private state: InterstitialState;
  private active: { id: string; placement: string; impressed: boolean; started: number; next: () => void; cleanup: () => void } | null = null;
  private foreground = true;
  private healthy = true;
  private readonly now: () => number;
  constructor(private readonly options: InterstitialOptions) {
    this.now = options.now ?? Date.now;
    const now = this.now();
    this.state = { day: this.day(now), daily: 0, session: 0, reservedSession: 0, reservedDaily: 0, lastActivity: now, lastClosed: 0, remainingCompletions: 0, seen: [], pending: null };
    try {
      const saved = options.store.read();
      if (saved) {
        if (!this.valid(saved)) throw new Error('invalid_state');
        this.state = structuredClone(saved);
        // 노출 전 예약 뒤 프로세스가 종료되면 노출 여부를 알 수 없다. 예약은 보수적으로 소비한다.
        if (this.state.pending) {
          this.state.reservedSession++; this.state.reservedDaily++;
          this.state.remainingCompletions = options.policy.completions_between - 1;
          this.state.lastClosed = now;
          this.state.pending = null;
        }
      }
      this.touch();
    } catch { this.healthy = false; }
  }
  private valid(s: InterstitialState): boolean {
    return /^\d{4}-\d{2}-\d{2}$/.test(s.day) && [s.daily,s.session,s.reservedSession,s.reservedDaily,s.lastActivity,s.lastClosed,s.remainingCompletions].every(n => Number.isSafeInteger(n) && n >= 0)
      && Array.isArray(s.seen) && s.seen.every(v => typeof v === 'string') && (s.pending === null || typeof s.pending === 'string');
  }
  private day(now: number): string { return new Date(now).toISOString().slice(0, 10); }
  private persist(): boolean {
    try { this.options.store.write(structuredClone(this.state)); return true; }
    catch { this.healthy = false; return false; }
  }
  /** 광고 외부 이동 중에는 lastActivity를 갱신하여 새 세션이 생기지 않게 한다. */
  touch(): void {
    const now = Math.max(this.now(), this.state.lastActivity);
    if (!this.active && now - this.state.lastActivity >= this.options.policy.session_reset_s * 1000) { this.state.session = 0; this.state.reservedSession = 0; }
    const day = this.day(now);
    if (day > this.state.day) { this.state.day = day; this.state.daily = 0; this.state.reservedDaily = 0; }
    this.state.lastActivity = now;
    if (this.healthy) this.persist();
  }
  setForeground(value: boolean): void {
    this.foreground = value;
    this.touch();
    this.options.suspended?.(!value || this.active !== null);
  }
  private track(name: string, reason = '', placement = ''): void {
    try { this.options.track?.(name, { policy_version: this.options.policy.version, reason, placement }); } catch { /* 계측 실패는 진행을 막지 않는다. */ }
  }
  opportunity(o: CompletionOpportunity, next: () => void): void {
    let done = false;
    const once = () => { if (!done) { done = true; next(); } };
    this.track('ad_opportunity', '', o.placement);
    const skip = (reason: string) => { this.track('ad_skipped', reason, o.placement); once(); };
    if (this.active) { skip('busy'); return; }
    this.touch();
    if (!this.healthy) { skip('storage_failed'); return; }
    if (this.state.seen.includes(o.id)) { skip('duplicate'); return; }
    if (!o.saved || !o.confirmed) { skip('unsaved_or_unconfirmed'); return; }
    if (!o.tutorialComplete) { skip('tutorial'); return; }
    if (!this.options.policy.enabled || o.adRemoved) { skip(o.adRemoved ? 'ad_removed' : 'disabled'); return; }
    // 완료는 미준비/쿨다운에도 1회 소비한다. 로드가 끝나도 놓친 경계를 재생하지 않는다.
    this.state.seen.push(o.id);
    this.state.seen = this.state.seen.slice(-256);
    const blockedBoundary = this.state.remainingCompletions > 0;
    if (blockedBoundary) this.state.remainingCompletions--;
    if (!this.persist()) { skip('storage_failed'); return; }
    if (blockedBoundary) { skip('completion_interval'); return; }
    if (!this.foreground) { skip('background'); return; }
    if (this.state.session + this.state.reservedSession >= this.options.policy.session_cap || this.state.daily + this.state.reservedDaily >= this.options.policy.daily_cap) { skip('cap'); return; }
    if (this.state.lastClosed && this.now() - this.state.lastClosed < this.options.policy.cooldown_s * 1000) { skip('cooldown'); return; }
    if (!this.options.adapter.ready()) { skip('not_ready'); return; }
    this.state.pending = o.id;
    if (!this.persist()) { skip('storage_failed'); return; }
    const request = { id: o.id, placement: o.placement, impressed: false, started: 0, next: once, cleanup: () => {} };
    this.active = request;
    this.options.suspended?.(true);
    this.track('ad_requested', '', o.placement);
    try {
      const cleanup = this.options.adapter.show(event => {
        if (this.active !== request) return;
        if (event === 'impression' && !request.impressed) {
          request.impressed = true;
          this.state.session++; this.state.daily++;
          this.state.remainingCompletions = this.options.policy.completions_between - 1;
          this.state.pending = null;
          this.persist();
          this.track('ad_impression', '', o.placement);
        } else if (event === 'show' && !request.started) {
          request.started = this.now(); this.track('ad_show', '', o.placement);
        } else if (event === 'dismissed' || event === 'failedToShow' || event === 'error') {
          this.finish(event);
        }
      });
      if (this.active === request) request.cleanup = cleanup; else cleanup();
    } catch { this.finish('error'); }
  }
  private finish(event: InterstitialEvent): void {
    const request = this.active;
    if (!request) return;
    this.active = null;
    this.state.pending = null;
    if (request.impressed || request.started) this.state.lastClosed = this.now();
    // 광고 클릭으로 장시간 이탈하더라도 광고 종료를 세션 새 시작으로 세지 않는다.
    this.state.lastActivity = Math.max(this.now(), this.state.lastActivity);
    this.persist();
    request.cleanup();
    this.track(event === 'dismissed' ? 'ad_closed' : 'ad_failed', event, request.placement);
    try { this.options.track?.('ad_duration', { policy_version: this.options.policy.version, placement: request.placement, duration_ms: request.started ? Math.max(0, this.now()-request.started) : 0 }); } catch { /* 계측 실패는 진행을 막지 않는다. */ }
    this.options.suspended?.(!this.foreground);
    request.next();
  }
}
