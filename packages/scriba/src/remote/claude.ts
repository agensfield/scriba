import { existsSync } from 'node:fs'
import { readFile, writeFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { z } from 'zod'
import type { FetchLike, RemoteProbeResult } from './types.ts'

const CLIENT_ID = '9d1c250a-e61b-44d9-88ed-5944d1962f5e'
const REFRESH_URL = 'https://platform.claude.com/v1/oauth/token'
const USAGE_URL = 'https://api.anthropic.com/api/oauth/usage'
const REFRESH_BUFFER_MS = 5 * 60 * 1000

const claudeCredentialSchema = z.object({
	claudeAiOauth: z.object({
		accessToken: z.string().optional(),
		refreshToken: z.string().optional(),
		expiresAt: z.number().optional(),
		scopes: z.array(z.string()).optional(),
	}),
})

const usageSchema = z.object({
	five_hour: claudeWindowSchema().optional(),
	seven_day: claudeWindowSchema().optional(),
	seven_day_opus: claudeWindowSchema().optional(),
	seven_day_omelette: claudeWindowSchema().optional(),
	extra_usage: z
		.object({
			is_enabled: z.boolean().optional(),
			used_credits: z.number().optional(),
			monthly_limit: z.number().optional(),
			currency: z.string().optional(),
		})
		.optional(),
})

function claudeWindowSchema() {
	return z.object({
		utilization: z.number(),
		resets_at: z.string().optional(),
	})
}

export type ClaudeProbeOptions = {
	env?: Record<string, string | undefined> | undefined
	fetch?: FetchLike | undefined
	now?: Date | undefined
	credentialPaths?: string[] | undefined
}

export function claudeCredentialPaths(
	env: Record<string, string | undefined> = process.env,
): string[] {
	const configured = env.CLAUDE_CONFIG_DIR?.trim()
	if (configured != null && configured !== '') {
		return configured.split(',').map((path) => join(path.trim(), '.credentials.json'))
	}
	return [join(homedir(), '.claude', '.credentials.json')]
}

export async function probeClaudeUsage(
	options: ClaudeProbeOptions = {},
): Promise<RemoteProbeResult> {
	const now = options.now ?? new Date()
	const fetchImpl = options.fetch ?? fetch
	const auth = await loadClaudeAuth(options)
	if (!auth.ok) {
		return {
			providerId: 'claude',
			lines: [{ type: 'badge', label: 'Claude API', text: 'Auth unavailable' }],
			provenance: [
				{
					kind: 'provider-api',
					providerId: 'claude',
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
			'Content-Type': 'application/json',
			'anthropic-beta': 'oauth-2025-04-20',
		},
	})
	if (!resp.ok) {
		throw new Error(`Claude usage request failed: ${resp.status}`)
	}
	const parsed = usageSchema.parse(await resp.json())
	const lines = []
	if (parsed.five_hour != null) {
		lines.push(progressLine('Session', parsed.five_hour))
	}
	if (parsed.seven_day != null) {
		lines.push(progressLine('Weekly', parsed.seven_day))
	}
	if (parsed.seven_day_opus != null) {
		lines.push(progressLine('Sonnet', parsed.seven_day_opus))
	}
	if (parsed.seven_day_omelette != null) {
		lines.push(progressLine('Claude Design', parsed.seven_day_omelette))
	}
	if (parsed.extra_usage?.is_enabled === true) {
		lines.push({
			type: 'progress' as const,
			label: 'Extra usage spent',
			used: (parsed.extra_usage.used_credits ?? 0) / 100,
			limit: Math.max(1, (parsed.extra_usage.monthly_limit ?? 0) / 100),
			format: { kind: 'dollars' as const },
		})
	}

	return {
		providerId: 'claude',
		lines,
		provenance: [{ kind: 'provider-api', providerId: 'claude', fetchedAt: now.toISOString() }],
		authState: auth,
	}
}

async function loadClaudeAuth(options: ClaudeProbeOptions) {
	const paths = options.credentialPaths ?? claudeCredentialPaths(options.env)
	for (const path of paths) {
		if (!existsSync(path)) {
			continue
		}
		const credentials = claudeCredentialSchema.parse(JSON.parse(await readFile(path, 'utf8')))
		const oauth = credentials.claudeAiOauth
		const accessToken = oauth.accessToken
		const refreshToken = oauth.refreshToken
		if (accessToken == null || accessToken === '') {
			continue
		}
		if (refreshToken != null && shouldRefresh(oauth.expiresAt, options.now ?? new Date())) {
			const refreshed = await refreshClaudeToken(credentials, refreshToken, options.fetch ?? fetch)
			if (refreshed != null) {
				await writeFile(path, `${JSON.stringify(refreshed, null, 2)}\n`)
				return {
					ok: true as const,
					accessToken: refreshed.claudeAiOauth.accessToken ?? accessToken,
					source: path,
				}
			}
		}
		return { ok: true as const, accessToken, source: path }
	}
	return { ok: false as const, error: 'Not logged in. Run `claude` to authenticate.' }
}

function shouldRefresh(expiresAt: number | undefined, now: Date): boolean {
	return expiresAt == null || expiresAt - now.getTime() < REFRESH_BUFFER_MS
}

async function refreshClaudeToken(
	credentials: z.infer<typeof claudeCredentialSchema>,
	refreshToken: string,
	fetchImpl: FetchLike,
) {
	const resp = await fetchImpl(REFRESH_URL, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({
			grant_type: 'refresh_token',
			refresh_token: refreshToken,
			client_id: CLIENT_ID,
		}),
	})
	if (!resp.ok) {
		return null
	}
	const json = (await resp.json()) as Record<string, unknown>
	if (typeof json.access_token !== 'string') {
		return null
	}
	return {
		...credentials,
		claudeAiOauth: {
			...credentials.claudeAiOauth,
			accessToken: json.access_token,
			refreshToken:
				typeof json.refresh_token === 'string'
					? json.refresh_token
					: credentials.claudeAiOauth.refreshToken,
			expiresAt:
				typeof json.expires_in === 'number'
					? Date.now() + json.expires_in * 1000
					: credentials.claudeAiOauth.expiresAt,
		},
	}
}

function progressLine(label: string, window: z.infer<ReturnType<typeof claudeWindowSchema>>) {
	return {
		type: 'progress' as const,
		label,
		used: window.utilization,
		limit: 100,
		format: { kind: 'percent' as const },
		resetsAt: window.resets_at,
	}
}
