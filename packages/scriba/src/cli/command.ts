import { defineCommand } from 'citty'
import { buildCcusageBenchmark } from '../bench/ccusage.ts'
import { resetCache, ScribaCache } from '../cache/sqlite.ts'
import { loadConfig } from '../config/loader.ts'
import type { ScribaConfig } from '../config/schema.ts'
import { SCRIBA_PACKAGE_NAME } from '../constants.ts'
import { iterateClaudeEvents, scanClaudeLogs } from '../local/claude.ts'
import { iterateCodexEvents } from '../local/codex.ts'
import { emptyScannerStats, type LocalUsageEvent, type ScanResult } from '../local/types.ts'
import { buildClaudeBlocks } from '../reports/blocks.ts'
import {
	buildDailyReportFromAsync,
	buildMonthlyReportFromAsync,
	buildSessionReportFromAsync,
	buildWeeklyReportFromAsync,
} from '../reports/stream.ts'
import { buildJsonSchemaRegistry } from '../schema/json-schema.ts'
import { buildStatusSnapshot } from '../status/build.ts'
import { evaluateTelegramAlerts } from '../telegram/alerts.ts'
import { sendTelegramAlerts } from '../telegram/send.ts'
import { VERSION } from '../version.ts'
import { renderBenchmark, renderReport, renderStatus, renderTelegram } from './render.ts'

function notImplemented(command: string): never {
	throw new Error(`${command} is not implemented yet`)
}

function printJson(value: unknown) {
	console.log(JSON.stringify(value, null, 2))
}

function printOutput(args: CliArgs, value: unknown, human: () => string) {
	if (args.json === true) {
		printJson(value)
		return
	}
	console.log(human())
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
	'no-cache'?: boolean | undefined
	since?: string | undefined
	until?: string | undefined
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
	const scan = loaded.config.providers.claude.enabled
		? await scanClaudeLogs({ paths: explicitPaths(loaded.config.providers.claude.paths) })
		: { events: [], stats: emptyScannerStats() }
	return { config: loaded.config, scan }
}

async function loadClaudeStream(args: CliArgs) {
	const loaded = await loadCliConfig(args)
	const stats = emptyScannerStats()
	const events = loaded.config.providers.claude.enabled
		? filterAsyncEvents(
				iterateClaudeEvents({ paths: explicitPaths(loaded.config.providers.claude.paths), stats }),
				args,
			)
		: emptyEvents()
	return { config: loaded.config, stats, events }
}

async function loadCodexStream(args: CliArgs) {
	const loaded = await loadCliConfig(args)
	const stats = emptyScannerStats()
	const events = loaded.config.providers.codex.enabled
		? filterAsyncEvents(
				iterateCodexEvents({ paths: explicitPaths(loaded.config.providers.codex.paths), stats }),
				args,
			)
		: emptyEvents()
	return { config: loaded.config, stats, events }
}

async function* emptyEvents(): AsyncGenerator<LocalUsageEvent> {
	// Typed empty async iterable for disabled providers.
}

async function runClaudeSummary(args: CliArgs) {
	const loaded = await loadCliConfig(args)
	const built = await buildStatusSnapshot({
		config: {
			...loaded.config,
			providers: {
				...loaded.config.providers,
				codex: { ...loaded.config.providers.codex, enabled: false },
			},
		},
	})
	printOutput(args, built.snapshot, () => renderStatus(built.snapshot))
}

async function runCodexSummary(args: CliArgs) {
	const loaded = await loadCliConfig(args)
	const built = await buildStatusSnapshot({
		config: {
			...loaded.config,
			providers: {
				...loaded.config.providers,
				claude: { ...loaded.config.providers.claude, enabled: false },
			},
		},
	})
	printOutput(args, built.snapshot, () => renderStatus(built.snapshot))
}

async function runClaudeDaily(args: CliArgs) {
	const { config, stats, events } = await loadClaudeStream(args)
	const payload = {
		providerId: 'claude',
		stats,
		rows: await buildDailyReportFromAsync(events, reportOptions(config)),
	}
	printOutput(args, payload, () => renderReport('Claude Daily', payload))
}

async function runClaudeWeekly(args: CliArgs) {
	const { config, stats, events } = await loadClaudeStream(args)
	const payload = {
		providerId: 'claude',
		stats,
		rows: await buildWeeklyReportFromAsync(events, reportOptions(config)),
	}
	printOutput(args, payload, () => renderReport('Claude Weekly', payload))
}

async function runClaudeMonthly(args: CliArgs) {
	const { config, stats, events } = await loadClaudeStream(args)
	const payload = {
		providerId: 'claude',
		stats,
		rows: await buildMonthlyReportFromAsync(events, reportOptions(config)),
	}
	printOutput(args, payload, () => renderReport('Claude Monthly', payload))
}

async function runClaudeSessions(args: CliArgs) {
	const { config, stats, events } = await loadClaudeStream(args)
	const payload = {
		providerId: 'claude',
		stats,
		rows: await buildSessionReportFromAsync(events, reportOptions(config)),
	}
	printOutput(args, payload, () => renderReport('Claude Sessions', payload))
}

async function runClaudeBlocks(args: CliArgs) {
	const { scan } = await loadClaude(args)
	const payload = {
		providerId: 'claude',
		stats: scan.stats,
		rows: buildClaudeBlocks(filterEvents(scan.events, args)),
	}
	printOutput(args, payload, () => renderReport('Claude Blocks', payload))
}

async function runCodexDaily(args: CliArgs) {
	const { config, stats, events } = await loadCodexStream(args)
	const payload = {
		providerId: 'codex',
		stats,
		rows: await buildDailyReportFromAsync(events, reportOptions(config)),
	}
	printOutput(args, payload, () => renderReport('Codex Daily', payload))
}

async function runCodexWeekly(args: CliArgs) {
	const { config, stats, events } = await loadCodexStream(args)
	const payload = {
		providerId: 'codex',
		stats,
		rows: await buildWeeklyReportFromAsync(events, reportOptions(config)),
	}
	printOutput(args, payload, () => renderReport('Codex Weekly', payload))
}

async function runCodexMonthly(args: CliArgs) {
	const { config, stats, events } = await loadCodexStream(args)
	const payload = {
		providerId: 'codex',
		stats,
		rows: await buildMonthlyReportFromAsync(events, reportOptions(config)),
	}
	printOutput(args, payload, () => renderReport('Codex Monthly', payload))
}

async function runCodexSessions(args: CliArgs) {
	const { config, stats, events } = await loadCodexStream(args)
	const payload = {
		providerId: 'codex',
		stats,
		rows: await buildSessionReportFromAsync(events, reportOptions(config)),
	}
	printOutput(args, payload, () => renderReport('Codex Sessions', payload))
}

const claudeSubCommands = {
	summary: defineCommand({
		meta: { name: 'summary' },
		args: reportArgs,
		run: ({ args }) => runClaudeSummary(args),
	}),
	daily: defineCommand({
		meta: { name: 'daily' },
		args: reportArgs,
		run: ({ args }) => runClaudeDaily(args),
	}),
	weekly: defineCommand({
		meta: { name: 'weekly' },
		args: reportArgs,
		run: ({ args }) => runClaudeWeekly(args),
	}),
	monthly: defineCommand({
		meta: { name: 'monthly' },
		args: reportArgs,
		run: ({ args }) => runClaudeMonthly(args),
	}),
	sessions: defineCommand({
		meta: { name: 'sessions' },
		args: reportArgs,
		run: ({ args }) => runClaudeSessions(args),
	}),
	session: defineCommand({
		meta: { name: 'session' },
		args: reportArgs,
		run: ({ args }) => runClaudeSessions(args),
	}),
	blocks: defineCommand({
		meta: { name: 'blocks' },
		args: reportArgs,
		run: ({ args }) => runClaudeBlocks(args),
	}),
}

const codexSubCommands = {
	summary: defineCommand({
		meta: { name: 'summary' },
		args: reportArgs,
		run: ({ args }) => runCodexSummary(args),
	}),
	daily: defineCommand({
		meta: { name: 'daily' },
		args: reportArgs,
		run: ({ args }) => runCodexDaily(args),
	}),
	weekly: defineCommand({
		meta: { name: 'weekly' },
		args: reportArgs,
		run: ({ args }) => runCodexWeekly(args),
	}),
	monthly: defineCommand({
		meta: { name: 'monthly' },
		args: reportArgs,
		run: ({ args }) => runCodexMonthly(args),
	}),
	sessions: defineCommand({
		meta: { name: 'sessions' },
		args: reportArgs,
		run: ({ args }) => runCodexSessions(args),
	}),
	session: defineCommand({
		meta: { name: 'session' },
		args: reportArgs,
		run: ({ args }) => runCodexSessions(args),
	}),
}

const cacheSubCommands = {
	status: defineCommand({
		meta: { name: 'status' },
		args: globalArgs,
		async run({ args }) {
			const loaded = await loadConfig({
				configPath: typeof args.config === 'string' ? args.config : undefined,
			})
			const cache = await ScribaCache.open({
				cacheDir:
					typeof args['cache-dir'] === 'string' ? args['cache-dir'] : loaded.config.cacheDir,
			})
			printJson(cache.status())
			cache.close()
		},
	}),
	reset: defineCommand({
		meta: { name: 'reset' },
		args: globalArgs,
		async run({ args }) {
			const loaded = await loadConfig({
				configPath: typeof args.config === 'string' ? args.config : undefined,
			})
			const cacheDir = await resetCache({
				cacheDir:
					typeof args['cache-dir'] === 'string' ? args['cache-dir'] : loaded.config.cacheDir,
			})
			printJson({ ok: true, cacheDir })
		},
	}),
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
			printOutput(args, payload, () => renderBenchmark(payload))
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
			const built = await buildStatusSnapshot({ config: loaded.config })
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
			printOutput(args, payload, () => renderTelegram(payload))
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
			status: defineCommand({
				meta: {
					name: 'status',
					description: 'Show the composed Scriba status snapshot.',
				},
				args: globalArgs,
				async run({ args }) {
					const loaded = await loadConfig({
						configPath: typeof args.config === 'string' ? args.config : undefined,
					})
					const built = await buildStatusSnapshot({ config: loaded.config })
					if (args['no-cache'] !== true) {
						const cache = await ScribaCache.open({
							cacheDir:
								typeof args['cache-dir'] === 'string' ? args['cache-dir'] : loaded.config.cacheDir,
						})
						cache.saveSnapshot('status', built.snapshot, built.snapshot.generatedAt)
						if (built.scanStats.claude != null) {
							cache.saveScanStats('claude', built.scanStats.claude, built.snapshot.generatedAt)
						}
						if (built.scanStats.codex != null) {
							cache.saveScanStats('codex', built.scanStats.codex, built.snapshot.generatedAt)
						}
						await cache.writeJsonSnapshot('status', built.snapshot)
						cache.close()
					}
					printOutput(args, built.snapshot, () => renderStatus(built.snapshot))
				},
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
		run({ rawArgs }) {
			if (rawArgs.length === 0) {
				notImplemented('status')
			}
		},
	})
}

export const CLI_COMMANDS = {
	packageName: SCRIBA_PACKAGE_NAME,
	root: ['status', 'claude', 'codex', 'schema', 'cache', 'bench', 'telegram'],
	claude: Object.keys(claudeSubCommands),
	codex: Object.keys(codexSubCommands),
	cache: Object.keys(cacheSubCommands),
	bench: Object.keys(benchSubCommands),
	telegram: Object.keys(telegramSubCommands),
} as const
