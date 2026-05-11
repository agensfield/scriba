import { writeFile } from 'node:fs/promises'
import { defineCommand } from 'citty'
import { buildCcusageBenchmark } from '../bench/ccusage.ts'
import { resetCache, ScribaCache, settledCacheDatabaseSizeBytes } from '../cache/sqlite.ts'
import { loadConfig } from '../config/loader.ts'
import type { ScribaConfig } from '../config/schema.ts'
import { SCRIBA_PACKAGE_NAME } from '../constants.ts'
import { buildDoctorReport } from '../doctor/check.ts'
import { iterateCachedClaudeEvents, iterateCachedCodexEvents } from '../local/cached.ts'
import { iterateClaudeEvents, scanClaudeLogs } from '../local/claude.ts'
import { iterateCodexEvents } from '../local/codex.ts'
import { isDirectory, walkJsonlFiles } from '../local/files.ts'
import { emptyScannerStats, type LocalUsageEvent, type ScanResult } from '../local/types.ts'
import { redactForSharing } from '../privacy/redact.ts'
import { PROVIDER_DESCRIPTORS } from '../providers/descriptors.ts'
import { buildClaudeBlocks } from '../reports/blocks.ts'
import {
	buildDailyReportFromAsync,
	buildMonthlyReportFromAsync,
	buildSessionReportFromAsync,
	buildWeeklyReportFromAsync,
} from '../reports/stream.ts'
import { buildJsonSchemaRegistry } from '../schema/json-schema.ts'
import type { ProviderSnapshot, StatusSnapshot } from '../schema/model.ts'
import { buildStatusSnapshot } from '../status/build.ts'
import { evaluateTelegramAlerts } from '../telegram/alerts.ts'
import { sendTelegramAlerts } from '../telegram/send.ts'
import { VERSION } from '../version.ts'
import {
	renderBenchmark,
	renderDoctor,
	renderReport,
	renderStatus,
	renderTelegram,
} from './render.ts'

function printJson(value: unknown) {
	console.log(JSON.stringify(value, null, 2))
}

function printOutput<T>(args: CliArgs, value: T, human: (value: T) => string) {
	const outputValue = (args.redact === true ? redactForSharing(value) : value) as T
	if (args.json === true) {
		printJson(outputValue)
		return
	}
	console.log(human(outputValue))
}

const globalArgs = {
	json: {
		type: 'boolean',
		description: 'Emit machine-readable JSON output.',
		alias: 'j',
		default: false,
	},
	config: {
		type: 'string',
		description: 'Path to a Scriba config file.',
		valueHint: 'path',
	},
	'cache-dir': {
		type: 'string',
		description: 'Override the Scriba cache directory.',
		valueHint: 'path',
	},
	'no-cache': {
		type: 'boolean',
		description: 'Disable reading and writing derived cache state.',
		default: false,
	},
	'no-remote': {
		type: 'boolean',
		description: 'Skip provider API probes and use local/cache data only.',
		default: false,
	},
	fast: {
		type: 'boolean',
		description: 'Read the cached status snapshot without live scanning.',
		default: false,
	},
	redact: {
		type: 'boolean',
		description: 'Redact local paths, account identifiers, and emails from output.',
		default: false,
	},
} as const

const reportArgs = {
	...globalArgs,
	since: {
		type: 'string',
		description: 'Include events on or after this date or timestamp.',
		valueHint: 'date',
	},
	until: {
		type: 'string',
		description: 'Include events before this date or timestamp.',
		valueHint: 'date',
	},
} as const

const benchArgs = {
	...globalArgs,
	provider: {
		type: 'string',
		description: 'Provider to benchmark: all, claude, or codex.',
		default: 'all',
	},
	execute: {
		type: 'boolean',
		description: 'Execute ccusage commands. Without this, only print the plan and dataset summary.',
		default: false,
	},
	'timeout-ms': {
		type: 'string',
		description: 'Per-command timeout when --execute is enabled.',
		default: '30000',
	},
	out: {
		type: 'string',
		description: 'Write benchmark JSON artifact to this path.',
		valueHint: 'path',
	},
} as const

const telegramArgs = {
	...globalArgs,
	send: {
		type: 'boolean',
		description: 'Send matching alerts through Telegram.',
		default: false,
	},
} as const

type CliArgs = {
	json?: boolean | undefined
	config?: string | undefined
	'cache-dir'?: string | undefined
	cacheDir?: string | undefined
	'no-cache'?: boolean | undefined
	cache?: boolean | undefined
	noCache?: boolean | undefined
	noRemote?: boolean | undefined
	'no-remote'?: boolean | undefined
	remote?: boolean | undefined
	fast?: boolean | undefined
	redact?: boolean | undefined
	out?: string | undefined
	since?: string | undefined
	until?: string | undefined
}

function cacheDisabled(args: CliArgs): boolean {
	return args['no-cache'] === true || args.noCache === true || args.cache === false
}

function cacheDirArg(args: CliArgs, config: ScribaConfig): string | undefined {
	return typeof args['cache-dir'] === 'string'
		? args['cache-dir']
		: typeof args.cacheDir === 'string'
			? args.cacheDir
			: config.cacheDir
}

function remoteDisabled(args: CliArgs): boolean {
	return args['no-remote'] === true || args.noRemote === true || args.remote === false
}

function remoteOption(args: CliArgs): { includeRemote?: boolean } {
	return remoteDisabled(args) ? { includeRemote: false } : {}
}

function explicitPaths(paths: string[]): string[] | undefined {
	return paths.length > 0 ? paths : undefined
}

function normalizeDateBoundary(value: string | undefined, endOfDay = false): string | undefined {
	if (value == null || value.trim() === '') {
		return undefined
	}
	const trimmed = value.trim()
	if (/^\d{4}-\d{2}-\d{2}$/.test(trimmed)) {
		return `${trimmed}T${endOfDay ? '23:59:59.999' : '00:00:00.000'}Z`
	}
	return trimmed
}

function filterEvents(events: LocalUsageEvent[], args: CliArgs): LocalUsageEvent[] {
	const since = normalizeDateBoundary(args.since)
	const until = normalizeDateBoundary(args.until, true)
	return events.filter((event) => {
		if (since != null && event.timestamp < since) {
			return false
		}
		if (until != null && event.timestamp > until) {
			return false
		}
		return true
	})
}

async function* filterAsyncEvents(
	events: AsyncIterable<LocalUsageEvent>,
	args: CliArgs,
): AsyncGenerator<LocalUsageEvent> {
	const since = normalizeDateBoundary(args.since)
	const until = normalizeDateBoundary(args.until, true)
	for await (const event of events) {
		if (since != null && event.timestamp < since) {
			continue
		}
		if (until != null && event.timestamp > until) {
			continue
		}
		yield event
	}
}

function reportOptions(config: ScribaConfig) {
	return config.timezone == null
		? { order: 'desc' as const }
		: { timezone: config.timezone, order: 'desc' as const }
}

function normalizeProvider(value: unknown): 'all' | 'claude' | 'codex' {
	return value === 'claude' || value === 'codex' ? value : 'all'
}

function normalizeTimeoutMs(value: unknown): number {
	if (typeof value !== 'string') {
		return 30_000
	}
	const parsed = Number.parseInt(value, 10)
	return Number.isFinite(parsed) && parsed > 0 ? parsed : 30_000
}

async function loadCliConfig(args: CliArgs) {
	return loadConfig({
		configPath: typeof args.config === 'string' ? args.config : undefined,
	})
}

async function loadClaude(args: CliArgs): Promise<{ config: ScribaConfig; scan: ScanResult }> {
	const loaded = await loadCliConfig(args)
	if (!loaded.config.providers.claude.enabled) {
		return { config: loaded.config, scan: { events: [], stats: emptyScannerStats() } }
	}
	if (cacheDisabled(args)) {
		return {
			config: loaded.config,
			scan: await scanClaudeLogs({ paths: explicitPaths(loaded.config.providers.claude.paths) }),
		}
	}
	const cache = await ScribaCache.open({
		cacheDir: cacheDirArg(args, loaded.config),
	})
	try {
		const stats = emptyScannerStats()
		const events = []
		for await (const event of iterateCachedClaudeEvents({
			cache,
			paths: explicitPaths(loaded.config.providers.claude.paths),
			stats,
		})) {
			events.push(event)
		}
		return { config: loaded.config, scan: { events, stats } }
	} finally {
		cache.close()
	}
}

async function loadClaudeStream(args: CliArgs) {
	const loaded = await loadCliConfig(args)
	const stats = emptyScannerStats()
	if (!loaded.config.providers.claude.enabled) {
		return { config: loaded.config, stats, events: emptyEvents(), close: () => {} }
	}
	if (cacheDisabled(args)) {
		return {
			config: loaded.config,
			stats,
			events: filterAsyncEvents(
				iterateClaudeEvents({ paths: explicitPaths(loaded.config.providers.claude.paths), stats }),
				args,
			),
			close: () => {},
		}
	}
	const cache = await ScribaCache.open({
		cacheDir: cacheDirArg(args, loaded.config),
	})
	return {
		config: loaded.config,
		stats,
		events: filterAsyncEvents(
			iterateCachedClaudeEvents({
				cache,
				paths: explicitPaths(loaded.config.providers.claude.paths),
				stats,
			}),
			args,
		),
		close: () => cache.close(),
	}
}

async function loadCodexStream(args: CliArgs) {
	const loaded = await loadCliConfig(args)
	const stats = emptyScannerStats()
	if (!loaded.config.providers.codex.enabled) {
		return { config: loaded.config, stats, events: emptyEvents(), close: () => {} }
	}
	if (cacheDisabled(args)) {
		return {
			config: loaded.config,
			stats,
			events: filterAsyncEvents(
				iterateCodexEvents({ paths: explicitPaths(loaded.config.providers.codex.paths), stats }),
				args,
			),
			close: () => {},
		}
	}
	const cache = await ScribaCache.open({
		cacheDir: cacheDirArg(args, loaded.config),
	})
	return {
		config: loaded.config,
		stats,
		events: filterAsyncEvents(
			iterateCachedCodexEvents({
				cache,
				paths: explicitPaths(loaded.config.providers.codex.paths),
				stats,
			}),
			args,
		),
		close: () => cache.close(),
	}
}

async function* emptyEvents(): AsyncGenerator<LocalUsageEvent> {
	// Typed empty async iterable for disabled providers.
}

async function runClaudeSummary(args: CliArgs) {
	const loaded = await loadCliConfig(args)
	const built = await buildStatusSnapshotForCli(args, {
		config: {
			...loaded.config,
			providers: {
				...loaded.config.providers,
				codex: { ...loaded.config.providers.codex, enabled: false },
			},
		},
	})
	printOutput(args, built.snapshot, (snapshot) => renderStatus(snapshot))
}

async function runCodexSummary(args: CliArgs) {
	const loaded = await loadCliConfig(args)
	const built = await buildStatusSnapshotForCli(args, {
		config: {
			...loaded.config,
			providers: {
				...loaded.config.providers,
				claude: { ...loaded.config.providers.claude, enabled: false },
			},
		},
	})
	printOutput(args, built.snapshot, (snapshot) => renderStatus(snapshot))
}

async function runStatus(args: CliArgs) {
	const loaded = await loadConfig({
		configPath: typeof args.config === 'string' ? args.config : undefined,
	})
	if (!cacheDisabled(args)) {
		const cache = await ScribaCache.open({
			cacheDir: cacheDirArg(args, loaded.config),
		})
		try {
			const built =
				args.fast === true
					? {
							snapshot: cache.loadSnapshot<StatusSnapshot>('status'),
							scanStats: { claude: null, codex: null },
							fromCache: true,
						}
					: await buildStatusSnapshot({
							config: loaded.config,
							cache,
							...remoteOption(args),
						})
			if (built.snapshot == null) {
				throw new Error('No cached status snapshot found. Run `scriba status` first.')
			}
			if (!('fromCache' in built)) {
				await saveBuiltStatus(cache, built)
			}
			printOutput(args, built.snapshot, (snapshot) => renderStatus(snapshot))
		} finally {
			cache.close()
		}
		return
	}
	const built = await buildStatusSnapshot({
		config: loaded.config,
		...remoteOption(args),
	})
	printOutput(args, built.snapshot, (snapshot) => renderStatus(snapshot))
}

async function buildStatusSnapshotForCli(
	args: CliArgs,
	options: Parameters<typeof buildStatusSnapshot>[0],
) {
	if (cacheDisabled(args)) {
		return buildStatusSnapshot({ ...options, ...remoteOption(args) })
	}
	const cache = await ScribaCache.open({
		cacheDir: cacheDirArg(args, options.config),
	})
	try {
		if (args.fast === true) {
			const snapshot = cache.loadSnapshot<StatusSnapshot>('status')
			if (snapshot == null) {
				throw new Error('No cached status snapshot found. Run `scriba status` first.')
			}
			return { snapshot, scanStats: { claude: null, codex: null }, fromCache: true }
		}
		try {
			return await buildStatusSnapshot({
				...options,
				...remoteOption(args),
				cache,
			})
		} catch (error) {
			const snapshot = cache.loadSnapshot<StatusSnapshot>('status')
			if (snapshot == null) {
				throw error
			}
			return {
				snapshot: markSnapshotStale(snapshot, error),
				scanStats: { claude: null, codex: null },
				fromCache: true,
			}
		}
	} finally {
		cache.close()
	}
}

function markSnapshotStale(snapshot: StatusSnapshot, error: unknown): StatusSnapshot {
	const message = error instanceof Error ? error.message : String(error)
	return {
		...snapshot,
		providers: snapshot.providers.map((provider) => ({
			...provider,
			state: (provider.state === 'broken' ? 'broken' : 'degraded') as ProviderSnapshot['state'],
			provenance: [
				...provider.provenance,
				{
					kind: 'cache' as const,
					providerId: provider.providerId,
					fetchedAt: new Date().toISOString(),
					stale: true,
					error: message,
				},
			],
		})),
	}
}

function saveBuiltStatus(
	cache: ScribaCache,
	built: Awaited<ReturnType<typeof buildStatusSnapshot>>,
) {
	cache.saveSnapshot('status', built.snapshot, built.snapshot.generatedAt)
	if (built.scanStats.claude != null) {
		cache.saveScanStats('claude', built.scanStats.claude, built.snapshot.generatedAt)
	}
	if (built.scanStats.codex != null) {
		cache.saveScanStats('codex', built.scanStats.codex, built.snapshot.generatedAt)
	}
	return cache.writeJsonSnapshot('status', built.snapshot)
}

async function runClaudeDaily(args: CliArgs) {
	const loaded = await loadClaudeStream(args)
	try {
		const payload = {
			providerId: 'claude',
			stats: loaded.stats,
			rows: await buildDailyReportFromAsync(loaded.events, reportOptions(loaded.config)),
		}
		printOutput(args, payload, (report) => renderReport('Claude Daily', report))
	} finally {
		loaded.close()
	}
}

async function runClaudeWeekly(args: CliArgs) {
	const loaded = await loadClaudeStream(args)
	try {
		const payload = {
			providerId: 'claude',
			stats: loaded.stats,
			rows: await buildWeeklyReportFromAsync(loaded.events, reportOptions(loaded.config)),
		}
		printOutput(args, payload, (report) => renderReport('Claude Weekly', report))
	} finally {
		loaded.close()
	}
}

async function runClaudeMonthly(args: CliArgs) {
	const loaded = await loadClaudeStream(args)
	try {
		const payload = {
			providerId: 'claude',
			stats: loaded.stats,
			rows: await buildMonthlyReportFromAsync(loaded.events, reportOptions(loaded.config)),
		}
		printOutput(args, payload, (report) => renderReport('Claude Monthly', report))
	} finally {
		loaded.close()
	}
}

async function runClaudeSessions(args: CliArgs) {
	const loaded = await loadClaudeStream(args)
	try {
		const payload = {
			providerId: 'claude',
			stats: loaded.stats,
			rows: await buildSessionReportFromAsync(loaded.events, reportOptions(loaded.config)),
		}
		printOutput(args, payload, (report) => renderReport('Claude Sessions', report))
	} finally {
		loaded.close()
	}
}

async function runClaudeBlocks(args: CliArgs) {
	const { scan } = await loadClaude(args)
	const payload = {
		providerId: 'claude',
		stats: scan.stats,
		rows: buildClaudeBlocks(filterEvents(scan.events, args)),
	}
	printOutput(args, payload, (report) => renderReport('Claude Blocks', report))
}

async function runCodexDaily(args: CliArgs) {
	const loaded = await loadCodexStream(args)
	try {
		const payload = {
			providerId: 'codex',
			stats: loaded.stats,
			rows: await buildDailyReportFromAsync(loaded.events, reportOptions(loaded.config)),
		}
		printOutput(args, payload, (report) => renderReport('Codex Daily', report))
	} finally {
		loaded.close()
	}
}

async function runCodexWeekly(args: CliArgs) {
	const loaded = await loadCodexStream(args)
	try {
		const payload = {
			providerId: 'codex',
			stats: loaded.stats,
			rows: await buildWeeklyReportFromAsync(loaded.events, reportOptions(loaded.config)),
		}
		printOutput(args, payload, (report) => renderReport('Codex Weekly', report))
	} finally {
		loaded.close()
	}
}

async function runCodexMonthly(args: CliArgs) {
	const loaded = await loadCodexStream(args)
	try {
		const payload = {
			providerId: 'codex',
			stats: loaded.stats,
			rows: await buildMonthlyReportFromAsync(loaded.events, reportOptions(loaded.config)),
		}
		printOutput(args, payload, (report) => renderReport('Codex Monthly', report))
	} finally {
		loaded.close()
	}
}

async function runCodexSessions(args: CliArgs) {
	const loaded = await loadCodexStream(args)
	try {
		const payload = {
			providerId: 'codex',
			stats: loaded.stats,
			rows: await buildSessionReportFromAsync(loaded.events, reportOptions(loaded.config)),
		}
		printOutput(args, payload, (report) => renderReport('Codex Sessions', report))
	} finally {
		loaded.close()
	}
}

const claudeSubCommands = {
	summary: defineCommand({
		meta: { name: 'summary', description: 'Show total Claude Code token usage.' },
		args: reportArgs,
		run: ({ args }) => runClaudeSummary(args),
	}),
	daily: defineCommand({
		meta: { name: 'daily', description: 'Show Claude Code usage grouped by day.' },
		args: reportArgs,
		run: ({ args }) => runClaudeDaily(args),
	}),
	weekly: defineCommand({
		meta: { name: 'weekly', description: 'Show Claude Code usage grouped by week.' },
		args: reportArgs,
		run: ({ args }) => runClaudeWeekly(args),
	}),
	monthly: defineCommand({
		meta: { name: 'monthly', description: 'Show Claude Code usage grouped by month.' },
		args: reportArgs,
		run: ({ args }) => runClaudeMonthly(args),
	}),
	sessions: defineCommand({
		meta: { name: 'sessions', description: 'Show Claude Code usage grouped by session.' },
		args: reportArgs,
		run: ({ args }) => runClaudeSessions(args),
	}),
	session: defineCommand({
		meta: { name: 'session', description: 'Alias for Claude Code session usage.' },
		args: reportArgs,
		run: ({ args }) => runClaudeSessions(args),
	}),
	blocks: defineCommand({
		meta: { name: 'blocks', description: 'Show Claude Code usage grouped by billing block.' },
		args: reportArgs,
		run: ({ args }) => runClaudeBlocks(args),
	}),
}

const codexSubCommands = {
	summary: defineCommand({
		meta: { name: 'summary', description: 'Show total Codex token usage.' },
		args: reportArgs,
		run: ({ args }) => runCodexSummary(args),
	}),
	daily: defineCommand({
		meta: { name: 'daily', description: 'Show Codex usage grouped by day.' },
		args: reportArgs,
		run: ({ args }) => runCodexDaily(args),
	}),
	weekly: defineCommand({
		meta: { name: 'weekly', description: 'Show Codex usage grouped by week.' },
		args: reportArgs,
		run: ({ args }) => runCodexWeekly(args),
	}),
	monthly: defineCommand({
		meta: { name: 'monthly', description: 'Show Codex usage grouped by month.' },
		args: reportArgs,
		run: ({ args }) => runCodexMonthly(args),
	}),
	sessions: defineCommand({
		meta: { name: 'sessions', description: 'Show Codex usage grouped by session.' },
		args: reportArgs,
		run: ({ args }) => runCodexSessions(args),
	}),
	session: defineCommand({
		meta: { name: 'session', description: 'Alias for Codex session usage.' },
		args: reportArgs,
		run: ({ args }) => runCodexSessions(args),
	}),
}

const cacheSubCommands = {
	status: defineCommand({
		meta: { name: 'status', description: 'Show cache location, size, snapshots, and WAL state.' },
		args: globalArgs,
		async run({ args }) {
			const loaded = await loadConfig({
				configPath: typeof args.config === 'string' ? args.config : undefined,
			})
			const cache = await ScribaCache.open({
				cacheDir: cacheDirArg(args, loaded.config),
			})
			printJson(cache.status())
			cache.close()
		},
	}),
	reset: defineCommand({
		meta: { name: 'reset', description: 'Delete Scriba derived cache files.' },
		args: globalArgs,
		async run({ args }) {
			const loaded = await loadConfig({
				configPath: typeof args.config === 'string' ? args.config : undefined,
			})
			const cacheDir = await resetCache({
				cacheDir: cacheDirArg(args, loaded.config),
			})
			printJson({ ok: true, cacheDir })
		},
	}),
	prune: defineCommand({
		meta: { name: 'prune', description: 'Remove cached file events for deleted usage logs.' },
		args: globalArgs,
		async run({ args }) {
			const loaded = await loadCliConfig(args)
			const cache = await ScribaCache.open({ cacheDir: cacheDirArg(args, loaded.config) })
			try {
				const existingPaths = await collectExistingLogPaths(loaded.config)
				const pruned = cache.pruneFileEvents(existingPaths)
				printJson({ ok: true, pruned, remaining: cache.status().fileEvents })
			} finally {
				cache.close()
			}
		},
	}),
	vacuum: defineCommand({
		meta: { name: 'vacuum', description: 'Compact the cache database and truncate WAL files.' },
		args: globalArgs,
		async run({ args }) {
			const loaded = await loadCliConfig(args)
			const cache = await ScribaCache.open({ cacheDir: cacheDirArg(args, loaded.config) })
			let result: ReturnType<ScribaCache['vacuum']>
			let databasePath: string
			try {
				result = cache.vacuum()
				databasePath = cache.databasePath
			} finally {
				cache.close()
			}
			const afterBytes = await settledCacheDatabaseSizeBytes(databasePath)
			const deltaBytes = afterBytes - result.beforeBytes
			printJson({
				ok: true,
				beforeBytes: result.beforeBytes,
				afterBytes,
				deltaBytes,
				reclaimedBytes: Math.max(0, -deltaBytes),
				grewBytes: Math.max(0, deltaBytes),
			})
		},
	}),
}

async function collectExistingLogPaths(config: ScribaConfig): Promise<Set<string>> {
	const paths = new Set<string>()
	const dirs = [
		...(config.providers.claude.enabled
			? config.providers.claude.paths.length > 0
				? config.providers.claude.paths
				: PROVIDER_DESCRIPTORS.claude.defaultLocalPaths()
			: []),
		...(config.providers.codex.enabled
			? config.providers.codex.paths.length > 0
				? config.providers.codex.paths
				: PROVIDER_DESCRIPTORS.codex.defaultLocalPaths()
			: []),
	]
	for (const dir of dirs) {
		if (!(await isDirectory(dir))) {
			continue
		}
		for await (const filePath of walkJsonlFiles(dir)) {
			paths.add(filePath)
		}
	}
	return paths
}

const benchSubCommands = {
	ccusage: defineCommand({
		meta: {
			name: 'ccusage',
			description: 'Build or run a bounded ccusage/openusage baseline benchmark.',
		},
		args: benchArgs,
		async run({ args }) {
			const payload = await buildCcusageBenchmark({
				provider: normalizeProvider(args.provider),
				execute: args.execute === true,
				timeoutMs: normalizeTimeoutMs(args['timeout-ms']),
			})
			if (typeof args.out === 'string' && args.out !== '') {
				await writeFile(args.out, `${JSON.stringify(redactForSharing(payload), null, 2)}\n`)
			}
			printOutput(args, payload, (benchmark) => renderBenchmark(benchmark))
		},
	}),
}

const telegramSubCommands = {
	alerts: defineCommand({
		meta: {
			name: 'alerts',
			description: 'Evaluate Telegram alerts for the current status snapshot.',
		},
		args: telegramArgs,
		async run({ args }) {
			const loaded = await loadCliConfig(args)
			const built = await buildStatusSnapshotForCli(args, { config: loaded.config })
			const alerts = evaluateTelegramAlerts(built.snapshot, loaded.config.telegram)
			let sent = 0
			if (args.send === true && alerts.length > 0) {
				const botToken = process.env[loaded.config.telegram.botTokenEnv]
				const chatId = loaded.config.telegram.chatId
				if (botToken == null || botToken === '') {
					throw new Error(`Missing Telegram bot token env: ${loaded.config.telegram.botTokenEnv}`)
				}
				if (chatId == null || chatId === '') {
					throw new Error('Missing telegram.chatId in Scriba config')
				}
				sent = await sendTelegramAlerts({ botToken, chatId, alerts })
			}
			const payload = {
				generatedAt: built.snapshot.generatedAt,
				enabled: loaded.config.telegram.enabled,
				alerts,
				sent,
			}
			printOutput(args, payload, (telegram) => renderTelegram(telegram))
		},
	}),
}

export function createRootCommand() {
	return defineCommand({
		meta: {
			name: 'scriba',
			version: VERSION,
			description: 'Fast local usage tracking for Claude Code and Codex.',
		},
		args: globalArgs,
		subCommands: {
			doctor: defineCommand({
				meta: {
					name: 'doctor',
					description: 'Check local paths, auth, remote reachability, and cache health.',
				},
				args: globalArgs,
				async run({ args }) {
					const loaded = await loadCliConfig(args)
					const cache = await ScribaCache.open({ cacheDir: cacheDirArg(args, loaded.config) })
					try {
						const payload = await buildDoctorReport({
							config: loaded.config,
							cache,
							...remoteOption(args),
						})
						printOutput(args, payload, (doctor) => renderDoctor(doctor))
					} finally {
						cache.close()
					}
				},
			}),
			status: defineCommand({
				meta: {
					name: 'status',
					description: 'Show the composed Scriba status snapshot.',
				},
				args: globalArgs,
				run: ({ args }) => runStatus(args),
			}),
			claude: defineCommand({
				meta: {
					name: 'claude',
					description: 'Claude Code usage reports.',
				},
				subCommands: claudeSubCommands,
			}),
			codex: defineCommand({
				meta: {
					name: 'codex',
					description: 'Codex usage reports.',
				},
				subCommands: codexSubCommands,
			}),
			schema: defineCommand({
				meta: {
					name: 'schema',
					description: 'Print Scriba JSON schema metadata.',
				},
				run: () => {
					printJson(buildJsonSchemaRegistry())
				},
			}),
			cache: defineCommand({
				meta: {
					name: 'cache',
					description: 'Inspect or reset the derived Scriba cache.',
				},
				subCommands: cacheSubCommands,
			}),
			bench: defineCommand({
				meta: {
					name: 'bench',
					description: 'Benchmark reference tools against local usage history.',
				},
				subCommands: benchSubCommands,
			}),
			telegram: defineCommand({
				meta: {
					name: 'telegram',
					description: 'Evaluate or send Telegram alerts.',
				},
				subCommands: telegramSubCommands,
			}),
		},
		run({ rawArgs, args }) {
			if (rawArgs.length === 0) {
				return runStatus(args)
			}
		},
	})
}

export const CLI_COMMANDS = {
	packageName: SCRIBA_PACKAGE_NAME,
	root: ['doctor', 'status', 'claude', 'codex', 'schema', 'cache', 'bench', 'telegram'],
	claude: Object.keys(claudeSubCommands),
	codex: Object.keys(codexSubCommands),
	cache: Object.keys(cacheSubCommands),
	bench: Object.keys(benchSubCommands),
	telegram: Object.keys(telegramSubCommands),
} as const
