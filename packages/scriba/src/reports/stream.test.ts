import { describe, expect, it } from 'vitest'
import type { LocalUsageEvent } from '../local/types.ts'
import { buildDailyReport, buildSessionReport } from './local.ts'
import { buildDailyReportFromAsync, buildSessionReportFromAsync } from './stream.ts'

describe('streaming reports', () => {
	it('matches array daily and session aggregation without retaining raw events', async () => {
		const events: LocalUsageEvent[] = [
			event({ sessionId: 'a', timestamp: '2026-05-05T01:00:00.000Z', totalTokens: 10 }),
			event({ sessionId: 'a', timestamp: '2026-05-05T02:00:00.000Z', totalTokens: 15 }),
			event({ sessionId: 'b', timestamp: '2026-05-04T01:00:00.000Z', totalTokens: 20 }),
		]

		expect(await buildDailyReportFromAsync(asyncEvents(events), { order: 'desc' })).toEqual(
			buildDailyReport(events, { order: 'desc' }),
		)
		expect(await buildSessionReportFromAsync(asyncEvents(events))).toEqual(
			buildSessionReport(events),
		)
	})
})

async function* asyncEvents(events: LocalUsageEvent[]): AsyncGenerator<LocalUsageEvent> {
	for (const event of events) {
		yield event
	}
}

function event(overrides: Partial<LocalUsageEvent>): LocalUsageEvent {
	return {
		providerId: 'codex',
		sessionId: 'session',
		timestamp: '2026-05-05T00:00:00.000Z',
		model: 'gpt-5',
		inputTokens: 0,
		outputTokens: 0,
		cacheCreationTokens: 0,
		cacheReadTokens: 0,
		cachedInputTokens: 0,
		reasoningOutputTokens: 0,
		totalTokens: 0,
		costUSD: null,
		sourcePath: '/tmp/session.jsonl',
		...overrides,
	}
}
