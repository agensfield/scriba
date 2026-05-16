import { mkdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { writeClaudeUsageFixture, writeCodexUsageFixture } from '../test/fixtures.ts'
import { withTempDir } from '../test/temp.ts'
import { scanClaudeLogs } from './claude.ts'
import { scanCodexLogs } from './codex.ts'

describe('local scanners', () => {
	it('streams Claude JSONL and dedupes message/request pairs', async () => {
		await withTempDir('scriba-claude-', async (root) => {
			const fixture = await writeClaudeUsageFixture(root)

			const result = await scanClaudeLogs({ paths: [fixture.projectsDir] })
			expect(result.stats.files).toBe(1)
			expect(result.stats.lines).toBe(3)
			expect(result.stats.invalidLines).toBe(1)
			expect(result.stats.duplicates).toBe(1)
			expect(result.events).toHaveLength(1)
			expect(result.events[0]?.totalTokens).toBe(135)
			expect(result.events[0]?.costUSD).toBe(0.12)
		})
	})

	it('streams Codex JSONL and converts cumulative totals into deltas', async () => {
		await withTempDir('scriba-codex-', async (root) => {
			const fixture = await writeCodexUsageFixture(root)

			const result = await scanCodexLogs({ paths: [fixture.sessionsDir] })
			expect(result.events).toHaveLength(2)
			expect(result.events[0]?.inputTokens).toBe(100)
			expect(result.events[1]?.inputTokens).toBe(40)
			expect(result.events[1]?.totalTokens).toBe(50)
			expect(result.events[1]?.model).toBe('gpt-5.3-codex')
		})
	})

	it('keeps Codex parser stable for malformed JSONL and missing model metadata', async () => {
		await withTempDir('scriba-codex-weird-', async (root) => {
			const sessionsDir = join(root, 'sessions')
			await mkdir(sessionsDir, { recursive: true })
			await writeFile(
				join(sessionsDir, 'missing-model.jsonl'),
				[
					'not-json-token_count',
					JSON.stringify({
						type: 'event_msg',
						timestamp: '2026-05-05T10:00:00.000Z',
						payload: {
							type: 'token_count',
							info: {
								last_token_usage: {
									input_tokens: 10,
									output_tokens: 2,
									total_tokens: 12,
								},
							},
						},
					}),
				].join('\n'),
			)

			const result = await scanCodexLogs({ paths: [sessionsDir] })
			expect(result.stats.invalidLines).toBe(1)
			expect(result.events[0]?.model).toBe('gpt-5')
			expect(result.events[0]?.isFallbackModel).toBe(true)
		})
	})
})
