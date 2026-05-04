import { mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { scribaConfigSchema } from '../config/schema.ts'
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
		})
		const line = built.snapshot.providers[0]?.lines[0]
		expect(line?.type).toBe('text')
		expect(line?.type === 'text' ? line.value : null).toBe('120')
		expect(built.scanStats.claude?.events).toBe(1)
	})
})
