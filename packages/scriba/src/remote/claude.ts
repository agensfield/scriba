import { execFile } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync } from 'node:fs'
import { readFile, writeFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { promisify } from 'node:util'
import { z } from 'zod'
import type { MetricLine } from '../schema/model.ts'
import type { FetchLike, RemoteProbeResult } from './types.ts'

const CLIENT_ID = '9d1c250a-e61b-44d9-88ed-5944d1962f5e'
const REFRESH_URL = 'https://platform.claude.com/v1/oauth/token'
const USAGE_URL = 'https://api.anthropic.com/api/oauth/usage'
const REFRESH_BUFFER_MS = 5 * 60 * 1000
const KEYCHAIN_SERVICE_PREFIX = 'Claude Code'
const execFileAsync = promisify(execFile)

const claudeCredentialSchema = z.object({
	claudeAiOauth: z.object({
		accessToken: z.string().optional(),
		refreshToken: z.string().optional(),
		expiresAt: z.number().optional(),
		scopes: z.array(z.string()).optional(),
	}),
})

const usageSchema = z.object({
	five_hour: nullableClaudeWindowSchema(),
	seven_day: nullableClaudeWindowSchema(),
	seven_day_oauth_apps: nullableClaudeWindowSchema(),
	seven_day_sonnet: nullableClaudeWindowSchema(),
	seven_day_opus: nullableClaudeWindowSchema(),
	seven_day_design: nullableClaudeWindowSchema(),
	seven_day_claude_design: nullableClaudeWindowSchema(),
	seven_day_omelette: nullableClaudeWindowSchema(),
	claude_design: nullableClaudeWindowSchema(),
	design: nullableClaudeWindowSchema(),
	omelette: nullableClaudeWindowSchema(),
	omelette_promotional: nullableClaudeWindowSchema(),
	seven_day_routines: nullableClaudeWindowSchema(),
	seven_day_claude_routines: nullableClaudeWindowSchema(),
	claude_routines: nullableClaudeWindowSchema(),
	routines: nullableClaudeWindowSchema(),
	routine: nullableClaudeWindowSchema(),
	seven_day_cowork: nullableClaudeWindowSchema(),
	cowork: nullableClaudeWindowSchema(),
	iguana_necktie: nullableClaudeWindowSchema(),
	extra_usage: z
		.object({
			is_enabled: z.boolean().optional(),
			used_credits: z.number().nullable().optional(),
			monthly_limit: z.number().nullable().optional(),
			utilization: z.number().nullable().optional(),
			currency: z.string().optional(),
		})
		.nullable()
		.optional(),
})

function claudeWindowSchema() {
	return z.object({
		utilization: z.number().optional(),
		resets_at: z.string().nullable().optional(),
	})
}

function nullableClaudeWindowSchema() {
	return claudeWindowSchema().nullable().optional()
}

export type ClaudeProbeOptions = {
	env?: Record<string, string | undefined> | undefined
	fetch?: FetchLike | undefined
	now?: Date | undefined
	credentialPaths?: string[] | undefined
	keychainServices?: string[] | undefined
	readKeychainCredential?: ((service: string) => Promise<string | null>) | undefined
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

export function claudeKeychainServices(
	env: Record<string, string | undefined> = process.env,
): string[] {
	const base = `${KEYCHAIN_SERVICE_PREFIX}${claudeOauthFileSuffix(env)}-credentials`
	const configured = env.CLAUDE_CONFIG_DIR?.trim()
	const hash =
		configured == null || configured === ''
			? null
			: createHash('sha256').update(configured.normalize('NFC')).digest('hex').slice(0, 8)
	return hash == null ? [base] : [`${base}-${hash}`, base]
}

export async function claudeKeychainServiceExists(service: string): Promise<boolean> {
	if (process.platform !== 'darwin') {
		return false
	}
	try {
		await execFileAsync('security', ['find-generic-password', '-s', service], {
			timeout: 5000,
			maxBuffer: 1024 * 1024,
		})
		return true
	} catch {
		return false
	}
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
	pushWindow(lines, '5h limit', parsed.five_hour)
	pushWindow(lines, 'Weekly limit', parsed.seven_day)
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
	let credentialError: { ok: false; error: string; source?: string | undefined } | null = null
	const paths = options.credentialPaths ?? claudeCredentialPaths(options.env)
	for (const path of paths) {
		if (!existsSync(path)) {
			continue
		}
		const credentials = parseClaudeCredentials(await readFile(path, 'utf8'))
		if (credentials == null) {
			continue
		}
		const auth = await resolveClaudeCredentials(
			credentials,
			`file:${path}`,
			options,
			async (next) => {
				await writeFile(path, `${JSON.stringify(next, null, 2)}\n`)
			},
		)
		if (auth != null) {
			if (!auth.ok) {
				credentialError = auth
				continue
			}
			return auth
		}
	}
	const services = options.keychainServices ?? claudeKeychainServices(options.env)
	for (const service of services) {
		const text = await readClaudeKeychainCredential(service, options)
		if (text == null) {
			continue
		}
		const credentials = parseClaudeCredentials(text)
		if (credentials == null) {
			continue
		}
		const auth = await resolveClaudeCredentials(
			credentials,
			`keychain:${service}`,
			options,
			async (next) => {
				await writeClaudeKeychainCredential(service, next)
			},
		)
		if (auth != null) {
			if (!auth.ok) {
				credentialError = auth
				continue
			}
			return auth
		}
	}
	if (credentialError != null) {
		return credentialError
	}
	return { ok: false as const, error: 'Not logged in. Run `claude` to authenticate.' }
}

async function resolveClaudeCredentials(
	credentials: z.infer<typeof claudeCredentialSchema>,
	source: string,
	options: ClaudeProbeOptions,
	persist: (credentials: z.infer<typeof claudeCredentialSchema>) => Promise<void>,
) {
	const oauth = credentials.claudeAiOauth
	const accessToken = oauth.accessToken
	const refreshToken = oauth.refreshToken
	if (accessToken == null || accessToken === '') {
		return null
	}
	if (refreshToken != null && shouldRefresh(oauth.expiresAt, options.now ?? new Date())) {
		const refreshed = await refreshClaudeToken(credentials, refreshToken, options.fetch ?? fetch)
		if (refreshed != null) {
			await persist(refreshed)
			return {
				ok: true as const,
				accessToken: refreshed.claudeAiOauth.accessToken ?? accessToken,
				source,
			}
		}
		return {
			ok: false as const,
			error: 'Claude OAuth credentials found but refresh failed. Run `claude` to re-authenticate.',
			source,
		}
	}
	return { ok: true as const, accessToken, source }
}

function parseClaudeCredentials(text: string) {
	const direct = parseClaudeCredentialJson(text)
	if (direct != null) {
		return direct
	}
	const decoded = decodeHexJson(text)
	return decoded == null ? null : parseClaudeCredentialJson(decoded)
}

function parseClaudeCredentialJson(text: string) {
	try {
		return claudeCredentialSchema.parse(JSON.parse(text))
	} catch {
		return null
	}
}

function decodeHexJson(text: string): string | null {
	const trimmed = text.trim()
	const hex = trimmed.startsWith('0x') ? trimmed.slice(2) : trimmed
	if (hex.length === 0 || hex.length % 2 !== 0 || !/^[\da-f]+$/i.test(hex)) {
		return null
	}
	return Buffer.from(hex, 'hex').toString('utf8')
}

async function readClaudeKeychainCredential(
	service: string,
	options: ClaudeProbeOptions,
): Promise<string | null> {
	if (options.readKeychainCredential != null) {
		return options.readKeychainCredential(service)
	}
	if (process.platform !== 'darwin') {
		return null
	}
	const user = (options.env ?? process.env).USER?.trim()
	if (user != null && user !== '') {
		const currentUser = await readMacosKeychain(['-a', user, '-s', service, '-w'])
		if (currentUser != null) {
			return currentUser
		}
	}
	return readMacosKeychain(['-s', service, '-w'])
}

async function readMacosKeychain(args: string[]): Promise<string | null> {
	try {
		const { stdout } = await execFileAsync('security', ['find-generic-password', ...args], {
			timeout: 5000,
			maxBuffer: 1024 * 1024,
		})
		const value = stdout.trim()
		return value === '' ? null : value
	} catch {
		return null
	}
}

async function writeClaudeKeychainCredential(
	service: string,
	credentials: z.infer<typeof claudeCredentialSchema>,
): Promise<void> {
	if (process.platform !== 'darwin') {
		return
	}
	try {
		await execFileAsync(
			'security',
			['add-generic-password', '-U', '-s', service, '-w', JSON.stringify(credentials, null, 2)],
			{ timeout: 5000, maxBuffer: 1024 * 1024 },
		)
	} catch {
		// Keep the refreshed in-memory token even if Keychain persistence is denied.
	}
}

function claudeOauthFileSuffix(env: Record<string, string | undefined>): string {
	const userType = env.USER_TYPE?.trim()
	if (userType === 'ant' && env.USE_LOCAL_OAUTH === '1') {
		return '-local-oauth'
	}
	if (userType === 'ant' && env.USE_STAGING_OAUTH === '1') {
		return '-staging-oauth'
	}
	if (env.CLAUDE_CODE_CUSTOM_OAUTH_URL?.trim()) {
		return '-custom-oauth'
	}
	return ''
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
	window: z.infer<ReturnType<typeof claudeWindowSchema>> | null | undefined,
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
		resetsAt: window.resets_at ?? undefined,
	}
}
