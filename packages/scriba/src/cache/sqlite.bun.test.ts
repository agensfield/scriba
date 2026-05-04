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

		expect(cache.loadSnapshot<StatusSnapshot>('status')?.schemaVersion).toBe(SCHEMA_VERSION)
		expect(cache.status().scanStats[0]?.stats.events).toBe(4)
		cache.close()
		await resetCache({ cacheDir })
	})
})
