import { defineCommand } from 'citty'
import { resetCache, ScribaCache } from '../cache/sqlite.ts'
import { loadConfig } from '../config/loader.ts'
import type { ScribaConfig } from '../config/schema.ts'
import { SCRIBA_PACKAGE_NAME } from '../constants.ts'
import { scanClaudeLogs } from '../local/claude.ts'
import { scanCodexLogs } from '../local/codex.ts'
import { emptyScannerStats, type LocalUsageEvent, type ScanResult } from '../local/types.ts'
import { buildClaudeBlocks } from '../reports/blocks.ts'
import {
	buildDailyReport,
	buildMonthlyReport,
	buildSessionReport,
	buildWeeklyReport,
} from '../reports/local.ts'
import { buildJsonSchemaRegistry } from '../schema/json-schema.ts'
import { buildStatusSnapshot } from '../status/build.ts'
import { VERSION } from '../version.ts'

function notImplemented(command: string): never {
	throw new Error(`${command} is not implemented yet`)
}

function printJson(value: unknown) {
	console.log(JSON.stringify(value, null, 2))
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

type CliArgs = {
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

function reportOptions(config: ScribaConfig) {
	return config.timezone == null
		? { order: 'desc' as const }
		: { timezone: config.timezone, order: 'desc' as const }
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

async function loadCodex(args: CliArgs): Promise<{ config: ScribaConfig; scan: ScanResult }> {
	const loaded = await loadCliConfig(args)
	const scan = loaded.config.providers.codex.enabled
		? await scanCodexLogs({ paths: explicitPaths(loaded.config.providers.codex.paths) })
		: { events: [], stats: emptyScannerStats() }
	return { config: loaded.config, scan }
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
	printJson(built.snapshot)
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
	printJson(built.snapshot)
}

async function runClaudeDaily(args: CliArgs) {
	const { config, scan } = await loadClaude(args)
	printJson({
		providerId: 'claude',
		stats: scan.stats,
		rows: buildDailyReport(filterEvents(scan.events, args), reportOptions(config)),
	})
}

async function runClaudeWeekly(args: CliArgs) {
	const { config, scan } = await loadClaude(args)
	printJson({
		providerId: 'claude',
		stats: scan.stats,
		rows: buildWeeklyReport(filterEvents(scan.events, args), reportOptions(config)),
	})
}

async function runClaudeMonthly(args: CliArgs) {
	const { config, scan } = await loadClaude(args)
	printJson({
		providerId: 'claude',
		stats: scan.stats,
		rows: buildMonthlyReport(filterEvents(scan.events, args), reportOptions(config)),
	})
}

async function runClaudeSessions(args: CliArgs) {
	const { config, scan } = await loadClaude(args)
	printJson({
		providerId: 'claude',
		stats: scan.stats,
		rows: buildSessionReport(filterEvents(scan.events, args), reportOptions(config)),
	})
}

async function runClaudeBlocks(args: CliArgs) {
	const { scan } = await loadClaude(args)
	printJson({
		providerId: 'claude',
		stats: scan.stats,
		rows: buildClaudeBlocks(filterEvents(scan.events, args)),
	})
}

async function runCodexDaily(args: CliArgs) {
	const { config, scan } = await loadCodex(args)
	printJson({
		providerId: 'codex',
		stats: scan.stats,
		rows: buildDailyReport(filterEvents(scan.events, args), reportOptions(config)),
	})
}

async function runCodexMonthly(args: CliArgs) {
	const { config, scan } = await loadCodex(args)
	printJson({
		providerId: 'codex',
		stats: scan.stats,
		rows: buildMonthlyReport(filterEvents(scan.events, args), reportOptions(config)),
	})
}

async function runCodexSessions(args: CliArgs) {
	const { config, scan } = await loadCodex(args)
	printJson({
		providerId: 'codex',
		stats: scan.stats,
		rows: buildSessionReport(filterEvents(scan.events, args), reportOptions(config)),
	})
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
					printJson(built.snapshot)
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
	root: ['status', 'claude', 'codex', 'schema', 'cache'],
	claude: Object.keys(claudeSubCommands),
	codex: Object.keys(codexSubCommands),
	cache: Object.keys(cacheSubCommands),
} as const
