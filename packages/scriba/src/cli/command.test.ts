import { describe, expect, it } from 'vitest'
import { CLI_COMMANDS, createRootCommand } from './command.ts'

describe('CLI command surface', () => {
	it('declares the alpha command groups', () => {
		expect(CLI_COMMANDS.root).toEqual(['status', 'claude', 'codex', 'schema', 'cache'])
		expect(CLI_COMMANDS.claude).toEqual([
			'summary',
			'daily',
			'weekly',
			'monthly',
			'sessions',
			'blocks',
		])
		expect(CLI_COMMANDS.codex).toEqual(['summary', 'daily', 'monthly', 'sessions'])
		expect(CLI_COMMANDS.cache).toEqual(['status', 'reset'])
	})

	it('creates the citty root command', () => {
		expect(createRootCommand()).toBeTypeOf('object')
	})
})
