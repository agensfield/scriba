import { describe, expect, it } from 'bun:test'
import { mkdtemp } from 'node:fs/promises'
import { SCHEMA_VERSION, type StatusSnapshot } from '../schema/model.ts'
import { resetCache, ScribaCache } from './sqlite.ts'

describe('ScribaCache', () => {
	it('stores snapshots and scan stats in SQLite', async () => {
		const cacheDir = await mkdtemp('/tmp/scriba-cache-')
		const cache = await ScribaCache.open({ cacheDir })
		const snapshot: StatusSnapshot = {
			schemaVersion: SCHEMA_VERSION,
			generatedAt: '2026-05-05T00:00:00.000Z',
			providers: [],
		}
		cache.saveSnapshot('status', snapshot, snapshot.generatedAt)
		cache.saveScanStats(
			'claude',
			{
				files: 1,
				bytes: 2,
				lines: 3,
				events: 4,
				invalidLines: 0,
				duplicates: 0,
				missingDirectories: [],
			},
			snapshot.generatedAt,
		)
		cache.saveFileEvents(
			'codex',
			'/tmp/session.jsonl',
			{ size: 10, mtimeMs: 20 },
			[{ id: 'event-1' }],
			{
				files: 1,
				bytes: 10,
				lines: 2,
				events: 1,
				invalidLines: 0,
				duplicates: 0,
				missingDirectories: [],
			},
			snapshot.generatedAt,
		)

		expect(cache.loadSnapshot<StatusSnapshot>('status')?.schemaVersion).toBe(SCHEMA_VERSION)
		expect(cache.status().scanStats[0]?.stats.events).toBe(4)
		expect(
			cache.loadFileEvents<{ id: string }>('codex', '/tmp/session.jsonl', {
				size: 10,
				mtimeMs: 20,
			})?.events[0]?.id,
		).toBe('event-1')
		expect(cache.status().fileEvents[0]?.files).toBe(1)
		expect(cache.status().schemaVersion).toBe(1)
		expect(cache.status().sizeBytes).toBeGreaterThan(0)
		cache.close()
		await resetCache({ cacheDir })
	})

	it('prunes stale file-event rows and vacuums', async () => {
		const cacheDir = await mkdtemp('/tmp/scriba-cache-')
		const cache = await ScribaCache.open({ cacheDir })
		cache.saveFileEvents(
			'codex',
			'/tmp/deleted.jsonl',
			{ size: 10, mtimeMs: 20 },
			[{ id: 'event-1' }],
			{
				files: 1,
				bytes: 10,
				lines: 1,
				events: 1,
				invalidLines: 0,
				duplicates: 0,
				missingDirectories: [],
			},
		)

		expect(cache.pruneFileEvents(new Set())).toBe(1)
		expect(cache.status().fileEvents).toHaveLength(0)
		cache.vacuum()
		cache.close()
		await resetCache({ cacheDir })
	})
})
