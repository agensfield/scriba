import { defineCommand } from 'citty'
import { SCRIBA_PACKAGE_NAME } from '../index.ts'
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
	status: defineCommand({ meta: { name: 'status' }, run: () => notImplemented('cache status') }),
	reset: defineCommand({ meta: { name: 'reset' }, run: () => notImplemented('cache reset') }),
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
				run: () => notImplemented('status'),
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
				run: () => notImplemented('schema'),
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
