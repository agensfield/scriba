import { existsSync } from 'node:fs'
import { readFile, writeFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { z } from 'zod'
import type { MetricLine } from '../schema/model.ts'
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
	seven_day_oauth_apps: claudeWindowSchema().optional(),
	seven_day_sonnet: claudeWindowSchema().optional(),
	seven_day_opus: claudeWindowSchema().optional(),
	seven_day_design: claudeWindowSchema().optional(),
	seven_day_claude_design: claudeWindowSchema().optional(),
	seven_day_omelette: claudeWindowSchema().optional(),
	claude_design: claudeWindowSchema().optional(),
	design: claudeWindowSchema().optional(),
	omelette: claudeWindowSchema().optional(),
	omelette_promotional: claudeWindowSchema().optional(),
	seven_day_routines: claudeWindowSchema().optional(),
	seven_day_claude_routines: claudeWindowSchema().optional(),
	claude_routines: claudeWindowSchema().optional(),
	routines: claudeWindowSchema().optional(),
	routine: claudeWindowSchema().optional(),
	seven_day_cowork: claudeWindowSchema().optional(),
	cowork: claudeWindowSchema().optional(),
	iguana_necktie: claudeWindowSchema().optional(),
	extra_usage: z
		.object({
			is_enabled: z.boolean().optional(),
			used_credits: z.number().optional(),
			monthly_limit: z.number().optional(),
			utilization: z.number().optional(),
			currency: z.string().optional(),
		})
		.optional(),
})

function claudeWindowSchema() {
	return z.object({
		utilization: z.number().optional(),
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
	const lines: MetricLine[] = []
	lines.push(claudePeakHoursLine(now))
	pushWindow(lines, 'Session', parsed.five_hour)
	pushWindow(lines, 'Weekly', parsed.seven_day)
	pushWindow(lines, 'OAuth Apps', parsed.seven_day_oauth_apps)
	pushWindow(lines, 'Sonnet', parsed.seven_day_sonnet ?? parsed.seven_day_opus)
	pushWindow(
		lines,
		'Claude Design',
		parsed.seven_day_design ??
			parsed.seven_day_claude_design ??
			parsed.claude_design ??
			parsed.design ??
			parsed.seven_day_omelette ??
			parsed.omelette ??
			parsed.omelette_promotional,
	)
	pushWindow(
		lines,
		'Claude Routines',
		parsed.seven_day_routines ??
			parsed.seven_day_claude_routines ??
			parsed.claude_routines ??
			parsed.routines ??
			parsed.routine ??
			parsed.seven_day_cowork ??
			parsed.cowork,
	)
	pushWindow(lines, 'Extra Claude window', parsed.iguana_necktie)
	if (parsed.extra_usage?.is_enabled === true) {
		const used = (parsed.extra_usage.used_credits ?? 0) / 100
		const limit = Math.max(1, (parsed.extra_usage.monthly_limit ?? 0) / 100)
		lines.push({
			type: 'progress' as const,
			label: 'Extra usage spent',
			used,
			limit,
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

function claudePeakHoursLine(now: Date): MetricLine {
	const minutes = newYorkMinutes(now)
	const peakStart = 8 * 60
	const peakEnd = 14 * 60
	if (minutes >= peakStart && minutes < peakEnd) {
		return {
			type: 'badge',
			label: 'Peak Hours',
			text: `Peak · ${durationLabel(peakEnd - minutes)} left`,
		}
	}
	const untilPeak = minutes < peakStart ? peakStart - minutes : 24 * 60 - minutes + peakStart
	return {
		type: 'badge',
		label: 'Peak Hours',
		text: `Off-peak · peak in ${durationLabel(untilPeak)}`,
	}
}

function newYorkMinutes(now: Date): number {
	const parts = new Intl.DateTimeFormat('en-US', {
		timeZone: 'America/New_York',
		hour: 'numeric',
		minute: 'numeric',
		hourCycle: 'h23',
	}).formatToParts(now)
	const hour = Number(parts.find((part) => part.type === 'hour')?.value ?? 0)
	const minute = Number(parts.find((part) => part.type === 'minute')?.value ?? 0)
	return hour * 60 + minute
}

function durationLabel(minutes: number): string {
	const hours = Math.floor(minutes / 60)
	const mins = minutes % 60
	if (hours > 0 && mins > 0) {
		return `${hours}h ${mins}m`
	}
	if (hours > 0) {
		return `${hours}h`
	}
	return `${mins}m`
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

function pushWindow(
	lines: MetricLine[],
	label: string,
	window: z.infer<ReturnType<typeof claudeWindowSchema>> | undefined,
) {
	if (window?.utilization == null) {
		return
	}
	lines.push(progressLine(label, window))
}

function progressLine(label: string, window: z.infer<ReturnType<typeof claudeWindowSchema>>) {
	return {
		type: 'progress' as const,
		label,
		used: window.utilization ?? 0,
		limit: 100,
		format: { kind: 'percent' as const },
		resetsAt: window.resets_at,
	}
}
