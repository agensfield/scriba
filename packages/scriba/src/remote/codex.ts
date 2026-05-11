import { existsSync } from 'node:fs'
import { readFile, writeFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { z } from 'zod'
import type { MetricLine } from '../schema/model.ts'
import type { FetchLike, RemoteProbeResult } from './types.ts'

const CLIENT_ID = 'app_EMoamEEZ73f0CkXaXp7hrann'
const REFRESH_URL = 'https://auth.openai.com/oauth/token'
const USAGE_URL = 'https://chatgpt.com/backend-api/wham/usage'
const REFRESH_AGE_MS = 8 * 24 * 60 * 60 * 1000

const codexAuthSchema = z.object({
	OPENAI_API_KEY: z.string().nullable().optional(),
	tokens: z
		.object({
			access_token: z.string().optional(),
			refresh_token: z.string().optional(),
			id_token: z.string().optional(),
			account_id: z.string().optional(),
		})
		.optional(),
	last_refresh: z.string().optional(),
})

const usageSchema = z.object({
	plan_type: z.string().optional(),
	rate_limit: z
		.object({
			primary_window: windowSchema().optional(),
			secondary_window: windowSchema().optional(),
		})
		.nullable()
		.optional(),
	code_review_rate_limit: z
		.object({
			primary_window: windowSchema().optional(),
			secondary_window: windowSchema().optional(),
		})
		.nullable()
		.optional(),
	credits: z
		.object({
			has_credits: z.boolean().optional(),
			unlimited: z.boolean().optional(),
			balance: z.union([z.number(), z.string()]).optional(),
		})
		.nullable()
		.optional(),
})

function windowSchema() {
	return z.object({
		used_percent: z.number(),
		reset_at: z.number().optional(),
		limit_window_seconds: z.number().optional(),
	})
}

export type CodexProbeOptions = {
	env?: Record<string, string | undefined> | undefined
	fetch?: FetchLike | undefined
	now?: Date | undefined
	authPaths?: string[] | undefined
}

export function codexAuthPaths(env: Record<string, string | undefined> = process.env): string[] {
	const codexHome = env.CODEX_HOME?.trim()
	if (codexHome != null && codexHome !== '') {
		return [join(codexHome, 'auth.json')]
	}
	return [join(homedir(), '.config', 'codex', 'auth.json'), join(homedir(), '.codex', 'auth.json')]
}

export async function probeCodexUsage(options: CodexProbeOptions = {}): Promise<RemoteProbeResult> {
	const now = options.now ?? new Date()
	const fetchImpl = options.fetch ?? fetch
	const auth = await loadCodexAuth(options)
	if (!auth.ok) {
		return {
			providerId: 'codex',
			lines: [{ type: 'badge', label: 'Codex API', text: 'Auth unavailable' }],
			provenance: [
				{
					kind: 'provider-api',
					providerId: 'codex',
					fetchedAt: now.toISOString(),
					error: auth.error,
				},
			],
			authState: auth,
		}
	}

	const resp = await fetchImpl(USAGE_URL, {
		headers: {
			Authorization: `Bearer ${auth.accessToken}`,
			Accept: 'application/json',
			...(auth.accountId != null ? { 'ChatGPT-Account-Id': auth.accountId } : {}),
		},
	})
	if (!resp.ok) {
		throw new Error(`Codex usage request failed: ${resp.status}`)
	}
	const parsed = usageSchema.parse(await resp.json())
	const lines: MetricLine[] = []
	const primary = parsed.rate_limit?.primary_window
	const secondary = parsed.rate_limit?.secondary_window
	const reviews = parsed.code_review_rate_limit?.primary_window
	const weeklyReviews = parsed.code_review_rate_limit?.secondary_window
	if (parsed.plan_type != null && parsed.plan_type !== '') {
		lines.push({ type: 'badge', label: 'Plan', text: parsed.plan_type })
	}
	if (primary != null) {
		lines.push(progressLine('5h limit', primary))
	}
	if (secondary != null) {
		lines.push(progressLine('Weekly limit', secondary))
		lines.push(progressLine('Spark weekly', secondary))
	}
	if (primary != null) {
		lines.push(progressLine('Spark 5h', primary))
	}
	if (reviews != null) {
		lines.push(progressLine('Review 5h', reviews))
	}
	if (weeklyReviews != null) {
		lines.push(progressLine('Review weekly', weeklyReviews))
	}
	if (parsed.credits?.has_credits === true) {
		const balance = Number(parsed.credits.balance ?? 0)
		if (parsed.credits.unlimited === true) {
			lines.push({ type: 'badge', label: 'Credits', text: 'unlimited' })
		} else if (Number.isFinite(balance)) {
			lines.push(amountLine('Credits left', Math.max(0, balance)))
		}
	}

	return {
		providerId: 'codex',
		lines,
		provenance: [{ kind: 'provider-api', providerId: 'codex', fetchedAt: now.toISOString() }],
		authState: auth,
	}
}

async function loadCodexAuth(options: CodexProbeOptions) {
	const paths = options.authPaths ?? codexAuthPaths(options.env)
	for (const path of paths) {
		if (!existsSync(path)) {
			continue
		}
		const auth = codexAuthSchema.parse(JSON.parse(await readFile(path, 'utf8')))
		if (auth.OPENAI_API_KEY != null) {
			return { ok: false as const, error: 'Usage not available for API key auth.', source: path }
		}
		const accessToken = auth.tokens?.access_token
		const refreshToken = auth.tokens?.refresh_token
		if (accessToken == null || accessToken === '') {
			continue
		}
		if (refreshToken != null && needsRefresh(auth.last_refresh, options.now ?? new Date())) {
			const refreshed = await refreshCodexToken(auth, refreshToken, options.fetch ?? fetch)
			if (refreshed != null) {
				await writeFile(path, `${JSON.stringify(refreshed, null, 2)}\n`)
				return {
					ok: true as const,
					accessToken: refreshed.tokens?.access_token ?? accessToken,
					accountId: refreshed.tokens?.account_id,
					source: path,
				}
			}
		}
		return { ok: true as const, accessToken, accountId: auth.tokens?.account_id, source: path }
	}
	return { ok: false as const, error: 'Not logged in. Run `codex` to authenticate.' }
}

function needsRefresh(lastRefresh: string | undefined, now: Date): boolean {
	if (lastRefresh == null) {
		return true
	}
	const lastMs = new Date(lastRefresh).getTime()
	return Number.isNaN(lastMs) || now.getTime() - lastMs > REFRESH_AGE_MS
}

async function refreshCodexToken(
	auth: z.infer<typeof codexAuthSchema>,
	refreshToken: string,
	fetchImpl: FetchLike,
) {
	const body = new URLSearchParams({
		grant_type: 'refresh_token',
		client_id: CLIENT_ID,
		refresh_token: refreshToken,
	})
	const resp = await fetchImpl(REFRESH_URL, {
		method: 'POST',
		headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
		body,
	})
	if (!resp.ok) {
		return null
	}
	const json = (await resp.json()) as Record<string, unknown>
	if (typeof json.access_token !== 'string') {
		return null
	}
	return {
		...auth,
		tokens: {
			...auth.tokens,
			access_token: json.access_token,
			refresh_token:
				typeof json.refresh_token === 'string' ? json.refresh_token : auth.tokens?.refresh_token,
			id_token: typeof json.id_token === 'string' ? json.id_token : auth.tokens?.id_token,
		},
		last_refresh: new Date().toISOString(),
	}
}

function amountLine(label: string, value: number): MetricLine {
	return {
		type: 'amount',
		label,
		value,
		format: { kind: 'count', suffix: 'credits' },
	}
}

function progressLine(label: string, window: z.infer<ReturnType<typeof windowSchema>>) {
	return {
		type: 'progress' as const,
		label,
		used: window.used_percent,
		limit: 100,
		format: { kind: 'percent' as const },
		resetsAt: window.reset_at == null ? undefined : new Date(window.reset_at * 1000).toISOString(),
		periodDurationMs:
			window.limit_window_seconds == null ? undefined : window.limit_window_seconds * 1000,
	}
}
