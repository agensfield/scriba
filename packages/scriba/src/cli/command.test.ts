import { describe, expect, it } from 'vitest'
import { CLI_COMMANDS, createRootCommand } from './command.ts'

describe('CLI command surface', () => {
	it('declares the alpha command groups', () => {
		expect(CLI_COMMANDS.root).toEqual([
			'doctor',
			'status',
			'claude',
			'codex',
			'schema',
			'cache',
			'bench',
			'telegram',
		])
		expect(CLI_COMMANDS.claude).toEqual([
			'summary',
			'daily',
			'weekly',
			'monthly',
			'sessions',
			'session',
			'blocks',
		])
		expect(CLI_COMMANDS.codex).toEqual([
			'summary',
			'daily',
			'weekly',
			'monthly',
			'sessions',
			'session',
		])
		expect(CLI_COMMANDS.cache).toEqual(['status', 'reset', 'prune', 'vacuum'])
		expect(CLI_COMMANDS.bench).toEqual(['ccusage'])
		expect(CLI_COMMANDS.telegram).toEqual(['alerts'])
	})

	it('creates the citty root command', () => {
		expect(createRootCommand()).toBeTypeOf('object')
	})

	it('defines help descriptions for every command', () => {
		const missing = commandDescriptions(createRootCommand())

		expect(missing).toEqual([])
	})
})

function commandDescriptions(command: unknown, path: string[] = []): string[] {
	if (command == null || typeof command !== 'object') {
		return []
	}
	const record = command as {
		meta?: { name?: string; description?: string }
		subCommands?: Record<string, unknown>
	}
	const name = record.meta?.name ?? path.at(-1) ?? '<root>'
	const nextPath = path.length === 0 ? [name] : [...path, name]
	const missing =
		typeof record.meta?.description === 'string' && record.meta.description.length > 0
			? []
			: [nextPath.join(' ')]
	const childMissing = Object.values(record.subCommands ?? {}).flatMap((child) =>
		commandDescriptions(child, nextPath),
	)
	return [...missing, ...childMissing]
}
