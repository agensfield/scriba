import { describe, expect, it } from 'vitest'
import { buildCcusageBenchmark, ccusageBenchmarkCommands } from './ccusage.ts'

describe('ccusage benchmark plan', () => {
	it('keeps the benchmark command set intentionally small', () => {
		expect(ccusageBenchmarkCommands('claude').map((command) => command.id)).toEqual([
			'ccusage-daily',
			'ccusage-weekly',
			'ccusage-monthly',
			'ccusage-sessions',
			'ccusage-blocks',
		])
		expect(ccusageBenchmarkCommands('codex').map((command) => command.id)).toEqual([
			'ccusage-codex-daily',
			'ccusage-codex-monthly',
			'ccusage-codex-sessions',
		])
	})

	it('does not execute reference commands unless requested', async () => {
		const result = await buildCcusageBenchmark({
			provider: 'claude',
			env: { CLAUDE_CONFIG_DIR: '/tmp/scriba-missing-claude' },
		})
		expect(result.execute).toBe(false)
		expect(result.results.every((command) => command.executed === false)).toBe(true)
		expect(result.datasets).toHaveLength(1)
		expect(result.datasets[0]?.missingDirectories).toEqual(['/tmp/scriba-missing-claude/projects'])
	})
})
