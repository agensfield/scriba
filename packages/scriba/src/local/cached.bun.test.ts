import { describe, expect, it } from 'bun:test'
import { mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { ScribaCache } from '../cache/sqlite.ts'
import { iterateCachedClaudeEvents, iterateCachedCodexEvents } from './cached.ts'
import { emptyScannerStats } from './types.ts'

describe('iterateCachedCodexEvents', () => {
	it('reuses cached parsed file events for unchanged files', async () => {
		const root = await mkdtemp('/tmp/scriba-cached-codex-')
		const sessionsDir = join(root, 'sessions')
		await mkdir(sessionsDir, { recursive: true })
		await writeFile(
			join(sessionsDir, 'session.jsonl'),
			`${JSON.stringify({
				type: 'event_msg',
				timestamp: '2026-05-05T10:00:00.000Z',
				payload: {
					type: 'token_count',
					info: {
						last_token_usage: {
							input_tokens: 100,
							cached_input_tokens: 10,
							output_tokens: 20,
							reasoning_output_tokens: 5,
							total_tokens: 120,
						},
					},
				},
			})}\n`,
		)
		const cache = await ScribaCache.open({ cacheDir: await mkdtemp('/tmp/scriba-cache-') })

		const coldStats = emptyScannerStats()
		const cold = []
		for await (const event of iterateCachedCodexEvents({
			cache,
			paths: [sessionsDir],
			stats: coldStats,
		})) {
			cold.push(event)
		}

		const warmStats = emptyScannerStats()
		const warm = []
		for await (const event of iterateCachedCodexEvents({
			cache,
			paths: [sessionsDir],
			stats: warmStats,
		})) {
			warm.push(event)
		}

		expect(cold).toHaveLength(1)
		expect(warm).toEqual(cold)
		expect(warmStats.events).toBe(1)
		expect(cache.status().fileEvents[0]?.files).toBe(1)
		cache.close()
	})
})

describe('iterateCachedClaudeEvents', () => {
	it('reuses cached parsed file events and preserves duplicate filtering', async () => {
		const root = await mkdtemp('/tmp/scriba-cached-claude-')
		const projectsDir = join(root, 'projects')
		const sessionDir = join(projectsDir, 'proj-a', 'session-a')
		await mkdir(sessionDir, { recursive: true })
		const entry = {
			timestamp: '2026-05-05T10:00:00.000Z',
			sessionId: 'session-a',
			requestId: 'req-1',
			message: {
				id: 'msg-1',
				model: 'claude-sonnet',
				usage: { input_tokens: 100, output_tokens: 20 },
			},
		}
		await writeFile(
			join(sessionDir, 'usage.jsonl'),
			`${JSON.stringify(entry)}\n${JSON.stringify(entry)}\n`,
		)
		const cache = await ScribaCache.open({ cacheDir: await mkdtemp('/tmp/scriba-cache-') })

		const coldStats = emptyScannerStats()
		const cold = []
		for await (const event of iterateCachedClaudeEvents({
			cache,
			paths: [projectsDir],
			stats: coldStats,
		})) {
			cold.push(event)
		}

		const warmStats = emptyScannerStats()
		const warm = []
		for await (const event of iterateCachedClaudeEvents({
			cache,
			paths: [projectsDir],
			stats: warmStats,
		})) {
			warm.push(event)
		}

		expect(cold).toHaveLength(1)
		expect(warm).toEqual(cold)
		expect(warmStats.duplicates).toBe(1)
		expect(cache.status().fileEvents.some((row) => row.providerId === 'claude')).toBe(true)
		cache.close()
	})
})
