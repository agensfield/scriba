import { mkdtemp } from 'node:fs/promises'
import { describe, expect, it } from 'vitest'
import { SCHEMA_VERSION, type StatusSnapshot } from '../schema/model.ts'
import { resetCache, ScribaCache } from './sqlite.ts'

describe('ScribaCache on Node', () => {
	it('stores snapshots through libsql', async () => {
		const cacheDir = await mkdtemp('/tmp/scriba-node-cache-')
		const cache = await ScribaCache.open({ cacheDir })
		const snapshot: StatusSnapshot = {
			schemaVersion: SCHEMA_VERSION,
			generatedAt: '2026-05-05T00:00:00.000Z',
			providers: [],
		}

		cache.saveSnapshot('status', snapshot, snapshot.generatedAt)

		expect(cache.loadSnapshot<StatusSnapshot>('status')?.schemaVersion).toBe(SCHEMA_VERSION)
		cache.close()
		await resetCache({ cacheDir })
	})
})
