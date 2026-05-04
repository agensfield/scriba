import { defineCommand } from 'citty'
import { resetCache, ScribaCache } from '../cache/sqlite.ts'
import { loadConfig } from '../config/loader.ts'
import { SCRIBA_PACKAGE_NAME } from '../constants.ts'
import { buildJsonSchemaRegistry } from '../schema/json-schema.ts'
import { buildStatusSnapshot } from '../status/build.ts'
import { VERSION } from '../version.ts'

function notImplemented(command: string): never {
	throw new Error(`${command} is not implemented yet`)
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

const claudeSubCommands = {
	summary: defineCommand({
		meta: { name: 'summary' },
		run: () => notImplemented('claude summary'),
	}),
	daily: defineCommand({ meta: { name: 'daily' }, run: () => notImplemented('claude daily') }),
	weekly: defineCommand({ meta: { name: 'weekly' }, run: () => notImplemented('claude weekly') }),
	monthly: defineCommand({
		meta: { name: 'monthly' },
		run: () => notImplemented('claude monthly'),
	}),
	sessions: defineCommand({
		meta: { name: 'sessions' },
		run: () => notImplemented('claude sessions'),
	}),
	blocks: defineCommand({ meta: { name: 'blocks' }, run: () => notImplemented('claude blocks') }),
}

const codexSubCommands = {
	summary: defineCommand({ meta: { name: 'summary' }, run: () => notImplemented('codex summary') }),
	daily: defineCommand({ meta: { name: 'daily' }, run: () => notImplemented('codex daily') }),
	monthly: defineCommand({ meta: { name: 'monthly' }, run: () => notImplemented('codex monthly') }),
	sessions: defineCommand({
		meta: { name: 'sessions' },
		run: () => notImplemented('codex sessions'),
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
			console.log(JSON.stringify(cache.status(), null, 2))
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
			console.log(JSON.stringify({ ok: true, cacheDir }, null, 2))
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
					console.log(JSON.stringify(built.snapshot, null, 2))
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
					console.log(JSON.stringify(buildJsonSchemaRegistry(), null, 2))
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
