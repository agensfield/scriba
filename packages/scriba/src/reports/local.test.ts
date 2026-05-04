import { describe, expect, it } from 'vitest'
import type { LocalUsageEvent } from '../local/types.ts'
import { buildClaudeBlocks } from './blocks.ts'
import {
	buildDailyReport,
	buildMonthlyReport,
	buildSessionReport,
	buildWeeklyReport,
} from './local.ts'

const events: LocalUsageEvent[] = [
	{
		providerId: 'claude',
		sessionId: 'a',
		timestamp: '2026-05-05T10:00:00.000Z',
		model: 'claude-sonnet',
		inputTokens: 100,
		outputTokens: 20,
		cacheCreationTokens: 10,
		cacheReadTokens: 5,
		cachedInputTokens: 5,
		reasoningOutputTokens: 0,
		totalTokens: 135,
		costUSD: 0.1,
		sourcePath: '/tmp/a.jsonl',
	},
	{
		providerId: 'claude',
		sessionId: 'a',
		timestamp: '2026-05-05T10:30:00.000Z',
		model: 'claude-sonnet',
		inputTokens: 50,
		outputTokens: 10,
		cacheCreationTokens: 0,
		cacheReadTokens: 5,
		cachedInputTokens: 5,
		reasoningOutputTokens: 0,
		totalTokens: 65,
		costUSD: 0.05,
		sourcePath: '/tmp/a.jsonl',
	},
]

describe('local reports', () => {
	it('aggregates daily, weekly, monthly, and session rows', () => {
		expect(buildDailyReport(events)[0]?.totalTokens).toBe(200)
		expect(buildWeeklyReport(events)[0]?.week).toBe('2026-05-04')
		expect(buildMonthlyReport(events)[0]?.month).toBe('2026-05')
		expect(buildSessionReport(events)[0]?.sessionId).toBe('a')
		expect(buildSessionReport(events)[0]?.models[0]?.costUSD).toBeCloseTo(0.15)
	})

	it('builds Claude 5h blocks', () => {
		const blocks = buildClaudeBlocks(events, { now: new Date('2026-05-05T11:00:00.000Z') })
		expect(blocks).toHaveLength(1)
		expect(blocks[0]?.isActive).toBe(true)
		expect(blocks[0]?.entries).toBe(2)
	})
})
