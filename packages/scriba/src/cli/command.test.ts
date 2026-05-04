import { describe, expect, it } from 'vitest'
import { CLI_COMMANDS, createRootCommand } from './command.ts'

describe('CLI command surface', () => {
	it('declares the alpha command groups', () => {
		expect(CLI_COMMANDS.root).toEqual([
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
		expect(CLI_COMMANDS.cache).toEqual(['status', 'reset'])
		expect(CLI_COMMANDS.bench).toEqual(['ccusage'])
		expect(CLI_COMMANDS.telegram).toEqual(['alerts'])
	})

	it('creates the citty root command', () => {
		expect(createRootCommand()).toBeTypeOf('object')
	})
})
