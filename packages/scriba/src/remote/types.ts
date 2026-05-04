import type { MetricLine, ProviderId, SourceProvenance } from '../schema/model.ts'

export type FetchLike = (input: string | URL | Request, init?: RequestInit) => Promise<Response>

export type AuthState =
	| { ok: true; accessToken: string; accountId?: string | undefined; source: string }
	| { ok: false; error: string; source?: string | undefined }

export type RemoteProbeResult = {
	providerId: ProviderId
	lines: MetricLine[]
	provenance: SourceProvenance[]
	authState: AuthState
}
