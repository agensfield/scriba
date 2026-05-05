import { mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { ScribaCache } from '../cache/sqlite.ts'
import { scribaConfigSchema } from '../config/schema.ts'
import type { ScannerStats } from '../local/types.ts'
import type { ProviderId } from '../schema/model.ts'
import { buildStatusSnapshot } from './build.ts'

describe('buildStatusSnapshot', () => {
	it('builds local-log provider lines', async () => {
		const root = await mkdtemp('/tmp/scriba-status-')
		const claudeDir = join(root, 'claude', 'proj', 'session')
		await mkdir(claudeDir, { recursive: true })
		await writeFile(
			join(claudeDir, 'usage.jsonl'),
			`${JSON.stringify({
				timestamp: '2026-05-05T10:00:00.000Z',
				sessionId: 'session',
				message: {
					id: 'm',
					model: 'claude-sonnet',
					usage: { input_tokens: 100, output_tokens: 20 },
				},
			})}\n`,
		)
		const config = scribaConfigSchema.parse({
			providers: {
				claude: { paths: [join(root, 'claude')] },
				codex: { enabled: false },
			},
		})

		const built = await buildStatusSnapshot({
			config,
			now: new Date('2026-05-05T12:00:00.000Z'),
			includeRemote: false,
		})
		const line = built.snapshot.providers[0]?.lines[0]
		expect(line?.type).toBe('text')
		expect(line?.type === 'text' ? line.value : null).toBe('120')
		expect(built.scanStats.claude?.events).toBe(1)
	})

	it('uses the derived file-event cache when provided', async () => {
		const root = await mkdtemp('/tmp/scriba-status-cache-')
		const codexDir = join(root, 'codex-sessions')
		await mkdir(codexDir, { recursive: true })
		await writeFile(
			join(codexDir, 'session.jsonl'),
			`${JSON.stringify({
				type: 'event_msg',
				timestamp: '2026-05-05T10:00:00.000Z',
				payload: {
					type: 'token_count',
					info: {
						last_token_usage: {
							input_tokens: 100,
							output_tokens: 20,
							total_tokens: 120,
						},
					},
				},
			})}\n`,
		)
		const cache = new MemoryFileEventCache()
		const config = scribaConfigSchema.parse({
			providers: {
				claude: { enabled: false },
				codex: { paths: [codexDir] },
			},
		})

		await buildStatusSnapshot({
			config,
			cache: cache as unknown as ScribaCache,
			now: new Date('2026-05-05T12:00:00.000Z'),
			includeRemote: false,
		})
		await buildStatusSnapshot({
			config,
			cache: cache as unknown as ScribaCache,
			now: new Date('2026-05-05T12:00:00.000Z'),
			includeRemote: false,
		})

		expect(cache.saveCount).toBe(1)
		expect(cache.hitCount).toBe(1)
	})
})

class MemoryFileEventCache {
	saveCount = 0
	hitCount = 0
	private events = new Map<
		string,
		{ fingerprint: { size: number; mtimeMs: number }; value: unknown }
	>()

	loadFileEvents<T>(
		providerId: ProviderId,
		path: string,
		fingerprint: { size: number; mtimeMs: number },
	) {
		const cached = this.events.get(`${providerId}:${path}`)
		if (
			cached == null ||
			cached.fingerprint.size !== fingerprint.size ||
			cached.fingerprint.mtimeMs !== fingerprint.mtimeMs
		) {
			return null
		}
		this.hitCount += 1
		return cached.value as { events: T[]; stats: ScannerStats }
	}

	saveFileEvents<T>(
		providerId: ProviderId,
		path: string,
		fingerprint: { size: number; mtimeMs: number },
		events: T[],
		stats: ScannerStats,
	) {
		this.saveCount += 1
		this.events.set(`${providerId}:${path}`, {
			fingerprint,
			value: { path, size: fingerprint.size, mtimeMs: fingerprint.mtimeMs, events, stats },
		})
	}
}
