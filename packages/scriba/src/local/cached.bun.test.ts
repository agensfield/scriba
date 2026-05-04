import { describe, expect, it } from 'bun:test'
import { mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { ScribaCache } from '../cache/sqlite.ts'
import { iterateCachedCodexEvents } from './cached.ts'
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
